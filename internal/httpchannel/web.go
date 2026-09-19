package httpchannel

const shareWebApp = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="light">
<title>send-cli</title>
<style>
:root{--bg:#f5f5f4;--card:#fff;--ink:#18181b;--muted:#71717a;--line:#e4e4e7;--soft:#f4f4f5;--accent:#18181b}
*{box-sizing:border-box}body{margin:0;min-height:100vh;background:var(--bg);color:var(--ink);font:14px/1.45 ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif}
button,textarea{font:inherit}.app{width:min(720px,calc(100% - 28px));height:min(820px,calc(100vh - 40px));min-height:520px;margin:20px auto;background:var(--card);border:1px solid var(--line);border-radius:16px;display:flex;flex-direction:column;overflow:hidden}
header{height:58px;flex:none;padding:0 18px;display:flex;align-items:center;justify-content:space-between;border-bottom:1px solid var(--line)}.brand{font-weight:700;letter-spacing:-.02em}.header-actions,.connection{display:flex;align-items:center}.header-actions{gap:12px}.connection{gap:7px;color:var(--muted);font-size:12px}.dot{width:7px;height:7px;border-radius:50%;background:#22c55e}.connection.offline .dot{background:#f59e0b}.qr-button{border:1px solid var(--line);border-radius:7px;background:white;color:var(--muted);padding:4px 7px;font-size:11px;cursor:pointer}.qr-button:hover{background:var(--soft);color:var(--ink)}
.feed{flex:1;overflow:auto;padding:18px;scroll-behavior:smooth}.empty{height:100%;display:grid;place-items:center;color:var(--muted);text-align:center}.event{display:flex;flex-direction:column;align-items:flex-start;margin:0 0 14px}.event.browser{align-items:flex-end}.meta{display:flex;gap:7px;margin:0 4px 5px;color:var(--muted);font-size:11px}.bubble{max-width:min(82%,560px);padding:9px 12px;background:var(--soft);border-radius:13px;white-space:pre-wrap;overflow-wrap:anywhere}.browser .bubble{background:var(--ink);color:white}.file{min-width:230px;max-width:min(88%,560px);display:flex;align-items:center;gap:11px;padding:10px 12px;border:1px solid var(--line);border-radius:13px;color:inherit;text-decoration:none;background:white}.file:hover{background:var(--soft)}.file-mark{width:34px;height:34px;flex:none;border-radius:9px;background:var(--soft);display:grid;place-items:center;font-size:17px}.file-copy{min-width:0}.file-name{font-weight:600;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.file-size{font-size:11px;color:var(--muted);margin-top:1px}
.upload-status{flex:none;padding:8px 18px;color:var(--muted);font-size:12px;border-top:1px solid var(--line)}.upload-status:empty{display:none}.compose{flex:none;border-top:1px solid var(--line);padding:12px;display:flex;align-items:flex-end;gap:8px}.attach,.send{height:40px;flex:none;border:0;border-radius:10px;cursor:pointer}.attach{width:40px;background:var(--soft);color:var(--ink);font-size:22px;line-height:1}.attach:hover{background:#e4e4e7}.message{min-height:40px;max-height:120px;resize:none;flex:1;border:0;border-radius:10px;background:var(--soft);padding:10px 12px;outline:none;color:var(--ink)}.message:focus{box-shadow:inset 0 0 0 1px #a1a1aa}.send{padding:0 15px;background:var(--accent);color:white;font-weight:600}.send:disabled,.attach:disabled{opacity:.45;cursor:wait}.app.drag{outline:2px solid var(--ink);outline-offset:-2px}
dialog{border:1px solid var(--line);border-radius:14px;padding:16px;margin:auto;color:var(--ink);text-align:center}dialog::backdrop{background:rgba(24,24,27,.28)}.qr-close{display:block;margin-left:auto;border:0;background:transparent;color:var(--muted);font-size:20px;line-height:1;cursor:pointer}.qr-image{display:block;width:220px;height:220px;margin:4px}.qr-caption{color:var(--muted);font-size:12px;margin-top:8px}
@media(max-width:600px){body{background:white}.app{width:100%;height:100dvh;min-height:0;margin:0;border:0;border-radius:0}.feed{padding:14px}.bubble{max-width:88%}.compose{padding:10px}}
</style>
</head>
<body>
<main class="app" id="app">
  <header><div class="brand">send-cli</div><div class="header-actions"><button class="qr-button" id="show-qr" type="button">QR</button><div class="connection" id="connection"><span class="dot"></span><span>Connected</span></div></div></header>
  <section class="feed" id="feed"><div class="empty" id="empty">No messages yet</div></section>
  <div class="upload-status" id="upload-status"></div>
  <form class="compose" id="message-form">
    <button class="attach" id="attach" type="button" aria-label="Choose files" title="Choose files">+</button>
    <input id="files" type="file" multiple hidden>
    <textarea class="message" id="message" maxlength="65536" rows="1" aria-label="Message" placeholder="Message"></textarea>
    <button class="send" type="submit">Send</button>
  </form>
</main>
<dialog id="qr-dialog"><button class="qr-close" id="close-qr" type="button" aria-label="Close">×</button><img class="qr-image" id="qr-image" alt="QR code for this share"><div class="qr-caption">Scan to open this share</div></dialog>
<script>
const base=location.pathname.replace(/\/$/,'');
const app=document.querySelector('#app'),feed=document.querySelector('#feed'),message=document.querySelector('#message'),form=document.querySelector('#message-form'),attach=document.querySelector('#attach'),files=document.querySelector('#files'),uploadStatus=document.querySelector('#upload-status'),connection=document.querySelector('#connection'),showQR=document.querySelector('#show-qr'),closeQR=document.querySelector('#close-qr'),qrDialog=document.querySelector('#qr-dialog'),qrImage=document.querySelector('#qr-image');
const seen=new Set();
const size=n=>{if(!n)return'0 B';const units=['B','KiB','MiB','GiB'],i=Math.min(Math.floor(Math.log(n)/Math.log(1024)),3),value=n/1024**i;return(i?value.toFixed(value<10?1:0):value)+' '+units[i]};
function addEvent(event){
  if(seen.has(event.id))return;seen.add(event.id);document.querySelector('#empty')?.remove();
  const row=document.createElement('article');row.className='event '+(event.sender==='Host'?'host':'browser');
  const meta=document.createElement('div');meta.className='meta';
  const who=document.createElement('span');who.textContent=event.sender||'Browser';
  const when=document.createElement('span');when.textContent=new Date(event.timestamp).toLocaleTimeString([],{hour:'2-digit',minute:'2-digit'});
  meta.append(who,when);row.append(meta);
  if(event.kind==='message'){
    const bubble=document.createElement('div');bubble.className='bubble';bubble.textContent=event.text;row.append(bubble);
  }else if(event.file){
    const link=document.createElement('a');link.className='file';link.href=event.file.url;link.download=event.file.name;
    const mark=document.createElement('span');mark.className='file-mark';mark.textContent='↓';
    const copy=document.createElement('span');copy.className='file-copy';
    const name=document.createElement('div');name.className='file-name';name.textContent=event.file.name;
    const bytes=document.createElement('div');bytes.className='file-size';bytes.textContent=size(event.file.size);
    copy.append(name,bytes);link.append(mark,copy);row.append(link);
  }
  feed.append(row);feed.scrollTop=feed.scrollHeight;
}
const stream=new EventSource(base+'/api/events');
stream.onmessage=event=>{connection.classList.remove('offline');connection.lastElementChild.textContent='Connected';addEvent(JSON.parse(event.data))};
stream.onerror=()=>{connection.classList.add('offline');connection.lastElementChild.textContent='Reconnecting'};
showQR.addEventListener('click',()=>{qrImage.src=base+'/api/qr';qrDialog.showModal()});
closeQR.addEventListener('click',()=>qrDialog.close());
qrDialog.addEventListener('click',event=>{if(event.target===qrDialog)qrDialog.close()});
form.addEventListener('submit',async event=>{
  event.preventDefault();const text=message.value.trim();if(!text)return;
  const button=form.querySelector('.send');button.disabled=true;
  try{const response=await fetch(base+'/api/message',{method:'POST',body:new URLSearchParams({message:text})});if(!response.ok)throw new Error(await response.text());message.value='';resizeMessage()}
  catch(error){uploadStatus.textContent='Could not send: '+error.message}
  finally{button.disabled=false;message.focus()}
});
function resizeMessage(){message.style.height='auto';message.style.height=Math.min(message.scrollHeight,120)+'px'}
message.addEventListener('input',resizeMessage);
message.addEventListener('keydown',event=>{if(event.key==='Enter'&&!event.shiftKey){event.preventDefault();form.requestSubmit()}});
attach.addEventListener('click',()=>files.click());files.addEventListener('change',()=>upload(files.files));
for(const name of ['dragenter','dragover'])app.addEventListener(name,event=>{event.preventDefault();app.classList.add('drag')});
for(const name of ['dragleave','drop'])app.addEventListener(name,event=>{event.preventDefault();app.classList.remove('drag')});
app.addEventListener('drop',event=>upload(event.dataTransfer.files));
async function upload(list){
  if(!list||!list.length)return;const data=new FormData();for(const file of list)data.append('files',file,file.name);
  attach.disabled=true;uploadStatus.textContent='Uploading '+list.length+(list.length===1?' file':' files')+'…';
  try{const response=await fetch(base+'/api/upload',{method:'POST',body:data});if(!response.ok)throw new Error(await response.text());uploadStatus.textContent='Uploaded';files.value='';setTimeout(()=>{if(uploadStatus.textContent==='Uploaded')uploadStatus.textContent=''},1800)}
  catch(error){uploadStatus.textContent='Upload failed: '+error.message}
  finally{attach.disabled=false}
}
</script>
</body>
</html>`
