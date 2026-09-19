package httpchannel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxErrorBody = 4 << 10

type Request struct {
	URL     string
	Method  string
	Message string
	Files   []string
	Headers []string
	Timeout time.Duration
}

type Result struct {
	StatusCode int
	Status     string
}

func Send(ctx context.Context, request Request) (Result, error) {
	endpoint, err := validateURL(request.URL)
	if err != nil {
		return Result{}, err
	}
	method := strings.ToUpper(strings.TrimSpace(request.Method))
	if method == "" {
		method = http.MethodPost
	}
	if strings.TrimSpace(request.Message) == "" && len(request.Files) == 0 {
		return Result{}, errors.New("provide a message with -m or at least one file")
	}
	files, err := validateFiles(request.Files)
	if err != nil {
		return Result{}, err
	}
	headers, err := parseHeaders(request.Headers)
	if err != nil {
		return Result{}, err
	}

	requestCtx := ctx
	cancel := func() {}
	if request.Timeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, request.Timeout)
	}
	defer cancel()

	var (
		body        io.Reader
		contentType string
		writeResult <-chan error
	)
	if len(files) == 0 {
		body = strings.NewReader(request.Message)
		contentType = "text/plain; charset=utf-8"
	} else {
		body, contentType, writeResult = multipartBody(requestCtx, request.Message, files)
	}
	httpRequest, err := http.NewRequestWithContext(requestCtx, method, endpoint.String(), body)
	if err != nil {
		return Result{}, err
	}
	for name, values := range headers {
		for _, value := range values {
			httpRequest.Header.Add(name, value)
		}
	}
	httpRequest.Header.Set("Content-Type", contentType)

	response, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		if closer, ok := body.(io.Closer); ok {
			_ = closer.Close()
		}
		if writeResult != nil {
			<-writeResult
		}
		return Result{}, err
	}
	defer response.Body.Close()
	if writeResult != nil {
		if err := <-writeResult; err != nil {
			return Result{}, err
		}
	}
	result := Result{StatusCode: response.StatusCode, Status: response.Status}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBody))
		detail := strings.TrimSpace(safeText(string(data)))
		if detail != "" {
			return result, fmt.Errorf("HTTP request failed: %s: %s", response.Status, detail)
		}
		return result, fmt.Errorf("HTTP request failed: %s", response.Status)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	return result, nil
}

func safeText(value string) string {
	return strings.Map(func(char rune) rune {
		if char == '\n' || char == '\t' || char >= ' ' && char != '\x7f' {
			return char
		}
		return -1
	}, value)
}

func validateURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return nil, fmt.Errorf("invalid HTTP URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("HTTP URL must use http:// or https://")
	}
	if parsed.Host == "" {
		return nil, errors.New("HTTP URL must include a host")
	}
	return parsed, nil
}

func validateFiles(paths []string) ([]string, error) {
	files := make([]string, len(paths))
	for index, path := range paths {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve %q: %w", path, err)
		}
		info, err := os.Stat(absolute)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%q is not a regular file", path)
		}
		files[index] = absolute
	}
	return files, nil
}

func parseHeaders(values []string) (http.Header, error) {
	headers := make(http.Header)
	for _, value := range values {
		name, content, ok := strings.Cut(value, ":")
		name = strings.TrimSpace(name)
		content = strings.TrimSpace(content)
		if !ok || name == "" || strings.ContainsAny(name+content, "\r\n") {
			return nil, fmt.Errorf("invalid HTTP header %q; use 'Name: value'", value)
		}
		headers.Add(name, content)
	}
	return headers, nil
}

func multipartBody(ctx context.Context, message string, files []string) (io.Reader, string, <-chan error) {
	reader, pipe := io.Pipe()
	writer := multipart.NewWriter(pipe)
	result := make(chan error, 1)
	go func() {
		var writeErr error
		if message != "" {
			writeErr = writer.WriteField("message", message)
		}
		for _, path := range files {
			if writeErr != nil {
				break
			}
			if err := ctx.Err(); err != nil {
				writeErr = err
				break
			}
			file, err := os.Open(path)
			if err != nil {
				writeErr = err
				break
			}
			part, err := writer.CreateFormFile("files", filepath.Base(path))
			if err == nil {
				_, err = io.Copy(part, file)
			}
			closeErr := file.Close()
			if err != nil {
				writeErr = err
			} else if closeErr != nil {
				writeErr = closeErr
			}
		}
		if closeErr := writer.Close(); writeErr == nil {
			writeErr = closeErr
		}
		_ = pipe.CloseWithError(writeErr)
		result <- writeErr
	}()
	return reader, writer.FormDataContentType(), result
}
