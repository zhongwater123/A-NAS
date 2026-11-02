// Browser integration with the real companion API/SQLite and real Hermes JS
// client. Agent events, approvals, file APIs and member auth are synthetic.
// This verifies UI/protocol integration, not model quality or NAS execution.
const { chromium } = require("playwright");
const assert = require("node:assert/strict");
const { spawn, execFile } = require("node:child_process");
const { promisify } = require("node:util");
const fs = require("node:fs/promises");
const os = require("node:os");
const path = require("node:path");
const { once } = require("node:events");
const root = path.resolve(__dirname, "../..");
const stored = "20261102_090000_abcdef", live = "abcdef12";
const source = { id:"source",spaceId:"private",name:"团队会议.md",kind:"file",sizeBytes:100,modifiedAt:"2026-11-02T00:00:00Z" };
const result = { ...source, id:"result",name:"团队会议-纪要.md" };
const answer = "# 团队会议\n\n摘要：先做桌宠与 NAS 小窝。\n\n- [ ] 前端：接任务状态，负责人：待确认，日期：待确认。";
async function run() {
 const temp=await fs.mkdtemp(path.join(os.tmpdir(),"anas-task-"));
 const output=path.join(__dirname,"build/tasks");await fs.mkdir(output,{recursive:true});
 let apiProcess,vite,browser,pageDirectory;
 try {
  const binary=path.join(temp,"fixture");await promisify(execFile)(process.env.GO||"go",["build","-o",binary,"./internal/companionapi/testdata/server"],{cwd:root});
  apiProcess=spawn(binary,[temp],{stdio:["ignore","pipe","inherit"]});const api=(await once(apiProcess.stdout,"data"))[0].toString().trim();
  assert.match(api,/^http:\/\/127\.0\.0\.1:\d+$/);
  await fs.mkdir(path.join(root,"web/.tmp"),{recursive:true});pageDirectory=await fs.mkdtemp(path.join(root,"web/.tmp/task-"));
  const pagePath=path.relative(path.join(root,"web"),pageDirectory).split(path.sep).join("/");
  await fs.writeFile(path.join(pageDirectory,"index.html"),`<html><head><meta charset="UTF-8"></head><body><div id="root"></div><script type="module" src="/${pagePath}/entry.jsx"></script></body></html>`);
  await fs.writeFile(path.join(pageDirectory,"entry.jsx"),`
import React, { useState } from "react"; import { createRoot } from "react-dom/client";
import "/src/styles/tokens.css";
import { CompanionPanel } from "/src/companion/CompanionPanel.tsx";
import { CompanionDesktop } from "/src/companion/CompanionDesktop.tsx";
import { AssistantPanel } from "/src/assistant/AssistantPanel.tsx";
function Preview(){const [intent,setIntent]=useState();const Component=new URLSearchParams(location.search).has("desktop")?CompanionDesktop:CompanionPanel;return <><Component userId="browser-test-member" connected onOpenConversation={(id)=>setIntent({kind:"conversation",sessionId:id})}/>{intent&&<div data-vnas-original><AssistantPanel username="fixture" intent={intent}/></div>}</>}
createRoot(document.getElementById("root")).render(<Preview/>);
`);
  await promisify(execFile)(path.join(root,"web/node_modules/.bin/biome"),["check","--write",pageDirectory],{cwd:path.join(root,"web")});
  const {createServer}=await import(path.join(root,"web/node_modules/vite/dist/node/index.js"));
  vite=await createServer({root:path.join(root,"web"),configFile:path.join(root,"web/companion.vite.config.ts"),optimizeDeps:{include:["react","react-dom/client","openapi-fetch","@assistant-ui/react-streamdown","@assistant-ui/react","cronstrue","cronstrue/locales/zh_CN","diff","dom-accessibility-api","lucide-react","modern-screenshot","react-error-boundary"]},server:{host:"127.0.0.1",port:0,hmr:false,open:false,proxy:{"/api/v1/companion":api}}});await vite.listen();
  const origin=vite.resolvedUrls.local[0];browser=await chromium.launch({headless:true,channel:process.env.ANAS_TEST_BROWSER||"chrome"});
  const sockets=new Set();const errors=[];const rpc=[];const http=[];let messages=[],cards=[],running=false,saved=false,prompts=0;
  const broadcast=(type,payload)=>{for(const socket of sockets){try{socket.send(JSON.stringify({jsonrpc:"2.0",method:"event",params:{type,session_id:live,payload}}));}catch{}}};
  const recordAnswer=(content)=>{messages.push({id:messages.length+1,role:"assistant",content,timestamp:Date.now()/1000});};
  const mount=async(desktop=false)=>{
   const context=await browser.newContext({viewport:desktop?{width:350,height:280}:{width:920,height:1100}});const page=await context.newPage();page.on("pageerror",e=>{errors.push(String(e));console.error("Page error:",String(e));});
   await page.route("**/api/v1/**",async route=>{
    const request=route.request(),url=new URL(request.url()),p=url.pathname;const respond=json=>route.fulfill({json});
    if(p.startsWith("/api/v1/companion"))return route.continue();
    http.push(`${request.method()} ${p}`);
    if(p.endsWith("/assistant/status"))return respond({available:true});
    if(p.endsWith("/assistant/approvals"))return respond({approvals:cards});
    if(p.endsWith("/assistant/approvals/save/confirm")){
     assert.equal(request.method(),"POST");saved=true;running=false;cards=[{...cards[0],status:"applied",change:{...cards[0].change,status:"applied"}}];broadcast("anas.approval",cards[0]);recordAnswer("已按确认卡保存纪要。");broadcast("message.delta",{text:"已按确认卡保存纪要。"});broadcast("message.complete",{status:"complete"});return respond({approval:cards[0]});
    }
    if(p.endsWith("/assistant/sessions"))return respond({sessions:messages.length?[{id:stored,title:"团队会议整理",preview:"会议纪要",startedAt:1,messageCount:messages.length}]:[]});
    if(p.endsWith(`/assistant/sessions/${stored}/messages`))return respond({messages});
    if(p.endsWith("/notifications"))return respond({items:[]});
    if(p.endsWith("/spaces"))return respond({items:[{id:"private",kind:"private",ownerUserId:"browser-test-member",name:"我的空间",createdAt:"2026-11-02T00:00:00Z"}]});
    if(p.endsWith("/spaces/private/entries"))return respond({items:saved?[source,result]:[source]});
    if(p.endsWith("/files/result/content"))return route.fulfill({contentType:"text/plain",body:answer});
    errors.push(`Unexpected API ${p}`);return route.fulfill({status:404,json:{error:{code:"not_found",message:p}}});
   });
   await page.routeWebSocket("**/api/v1/assistant/ws",socket=>{
    sockets.add(socket);socket.onClose(()=>sockets.delete(socket));
    socket.onMessage(async raw=>{
     const frame=JSON.parse(String(raw));rpc.push(frame.method);let value={};
     if(frame.method==="session.create")value={session_id:live,stored_session_id:stored};
     if(frame.method==="session.resume")value={session_id:live,running};
     if(frame.method==="session.active_list")value={sessions:running?[{session_id:live,stored_session_id:stored,running:true}]:[]};
     if(frame.method==="session.events.since")value={events:[],truncated:false};
     if(frame.method==="prompt.submit"){
      const state=await (await fetch(`${api}/api/v1/companion`)).json();assert.equal(state.conversationId,stored);prompts++;running=true;messages.push({id:messages.length+1,role:"user",content:frame.params.text,timestamp:Date.now()/1000});
     }
     if(frame.method==="session.interrupt"){running=false;broadcast("message.complete",{status:"interrupted"});}
     socket.send(JSON.stringify({jsonrpc:"2.0",id:frame.id,result:value}));
     if(frame.method==="prompt.submit")setTimeout(()=>{broadcast("message.start",{});broadcast("tool.start",{name:prompts===1?"read_file":"submit_changes",tool_call_id:`call-${prompts}`,arguments:{path:"/nas/个人空间/团队会议.md"}});
      if(prompts===1){running=false;recordAnswer(answer);broadcast("message.delta",{text:answer});broadcast("message.complete",{status:"complete"});}
      else {cards=[{requestId:"save",kind:"change",sessionId:live,storedSessionId:stored,status:"awaiting",createdAt:Date.now()/1000,change:{id:"change-save",summary:"保存团队会议纪要",status:"awaiting",operations:[{op:"write",path:"/nas/个人空间/团队会议-纪要.md",after:answer}]}}];broadcast("anas.approval",cards[0]);}
     },200);
    });
    socket.send(JSON.stringify({jsonrpc:"2.0",method:"event",params:{type:"gateway.ready",payload:{replay_epoch:"fixture"}}}));
   });
   await page.goto(`${origin}${pagePath}/index.html${desktop?"?desktop=1":""}`);return page;
  };
  const home=await mount();const pet=home.getByRole("region",{name:"伙伴小窝",exact:true});await pet.getByRole("button",{name:"叫到这里"}).click();
  await pet.getByRole("button",{name:"团队会议.md",exact:true}).click();await pet.getByRole("button",{name:"整理这份记录",exact:true}).click();
  await pet.getByText(answer,{exact:true}).waitFor();await pet.getByRole("button",{name:"保存到个人空间"}).click();
  await pet.getByRole("button",{name:"去 VNAS 处理确认卡"}).waitFor();assert.equal(saved,false);assert.equal(prompts,2);
  await home.screenshot({path:path.join(output,"home-awaiting.png"),fullPage:true});
  const desktop=await mount(true);await desktop.locator('[data-task-phase="waiting"]').waitFor();
  await desktop.getByText("正在这里陪你",{exact:true}).waitFor();
  await desktop.screenshot({path:path.join(output,"desktop-awaiting.png")});
  // Return to the same original VNAS thread and click its real approval UI.
  await pet.getByRole("button",{name:"去 VNAS 处理确认卡"}).click();const original=home.locator("[data-vnas-original]");await original.getByRole("button",{name:"确认更改",exact:true}).click();
  await pet.getByRole("heading",{name:"已确认保存"}).waitFor();await pet.getByRole("button",{name:"打开文件预览"}).click();await pet.locator("li pre").filter({hasText:"# 团队会议"}).waitFor();
  assert.equal(http.filter(s=>s.endsWith("/confirm")).length,1);assert.equal(prompts,2);assert.equal(saved,true);
  await home.screenshot({path:path.join(output,"home-saved.png"),fullPage:true});
  await desktop.reload();await desktop.locator('[data-task-phase="success"]').waitFor();assert.equal(prompts,2);
  assert.deepEqual(errors,[]);
  await fs.writeFile(path.join(output,"result.json"),JSON.stringify({result:"passed",synthetic:["member auth","Hermes runtime/model","file APIs","approval executor"],real:["companion SQLite/API","React components","Hermes JsonRpcGatewayClient","original VNAS approval UI"],scenarios:["NAS minutes selection","shared identity before prompt","draft","save waits","desktop same waiting thread","original manual approval","file preview","reload no duplicate submission"],prompts,manualConfirmations:1,errors},null,2));
  console.log(`Task protocol smoke passed; evidence: ${output}`);
 }catch(error){
  if(browser){for(const context of browser.contexts()){for(const page of context.pages()){console.error("UI:", (await page.locator("body").innerText()).slice(0,1500));await page.screenshot({path:path.join(output,"failure.png"),fullPage:true});}}}
  throw error;
 }finally{if(browser)await browser.close();if(vite)await vite.close();if(apiProcess)apiProcess.kill("SIGINT");if(pageDirectory)await fs.rm(pageDirectory,{recursive:true,force:true});await fs.rm(temp,{recursive:true,force:true});}
}
run().catch(error=>{console.error(error);process.exitCode=1;});
