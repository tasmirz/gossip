'use strict';
mermaid.initialize({startOnLoad:false,securityLevel:'strict',maxTextSize:20000,flowchart:{htmlLabels:false},theme:'neutral'});
document.addEventListener('click',event=>event.preventDefault());
window.addEventListener('message',async event=>{
  if(event.source!==window.parent || typeof event.data!=='string' || event.data.length>20000)return;
  const source=event.data.trim();
  const target=document.getElementById('diagram'), error=document.getElementById('error');
  if(!/^(sequenceDiagram|stateDiagram-v2|classDiagram|flowchart|graph)\b/.test(source) || source.includes('%%{')){error.textContent='Unsupported or unsafe diagram source.';return;}
  try{const result=await mermaid.render('report-diagram',source);target.innerHTML=result.svg;error.textContent='';}
  catch(_){target.replaceChildren();error.textContent='Unable to render this diagram. The source remains available in the report.';}
});
