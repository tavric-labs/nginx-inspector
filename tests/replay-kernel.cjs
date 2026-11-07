const assert=require('node:assert/strict'),fs=require('node:fs');const {chromium}=require('playwright');
(async()=>{const browser=await chromium.launch({headless:true,args:['--no-sandbox']});try{
 const page=await browser.newPage({viewport:{width:1700,height:1100}}),errors=[];page.on('pageerror',e=>errors.push(e.message));
 const now=Date.now();function event(path,number,protocol){return {schema:2,id:'11-22-'+number,started_ms:now,duration_ms:8,method:'GET',path,status:200,response_body_bytes:20,phases_ms:{},upstream:{},native:{nginx:{worker_pid:11,event_backend:'epoll'},connection:{id:99,request_number:number,protocol},trace:[{event:'post_read',at_ms:0,subrequest:0},{event:'precontent',at_ms:2,subrequest:0},{event:'header_filter',at_ms:3,subrequest:0},{event:'log',at_ms:8,subrequest:0}],attempts:[],subrequests:[],policy:{},request_body:{}}};}
 const archived=event('/archived',99,'HTTP/1.1');
 const events=[event('/new',1,'HTTP/1.1'),event('/reused',5,'HTTP/1.1'),event('/multiplexed',9,'HTTP/2.0')];
 let workers={available:false,capture_active:true,processes:[],live_workers:[]};
 await page.route('http://inspector.test/**',async route=>{
  const url=new URL(route.request().url()),path=url.pathname;
  if(path==='/api/events')await new Promise(resolve=>setTimeout(resolve,250));
  const data={'/api/events':url.searchParams.get('transport')==='1'?[events[1],events[2]]:events,'/api/summary':{retained:3,window:3,errors:0},'/api/capture':{active:true,sample_permyriad:10000,remaining_seconds:60},'/api/workers':workers};
  const html=path==='/'?'collector/viewer.html':path==='/replay/detail'?'collector/journey.html':null;
  let body;if(html)body=fs.readFileSync(html,'utf8');else if(path.startsWith('/api/transport/'))body=JSON.stringify({status:'not_captured',records:[]});else if(path.startsWith('/api/events/')){const e=[...events,archived].find(e=>e.id===path.split('/').pop());if(!e){await route.fulfill({status:404,body:'{}'});return;}body=JSON.stringify(e);}else body=JSON.stringify(data[path]??{});
  await route.fulfill({contentType:html?'text/html':'application/json',body});
 });
 await page.goto('http://inspector.test/');await page.locator('#show-replay').click();
 const flow=page.frameLocator('#rp-flow-frame'),modal=page.locator('#deep-replay-dialog');
 await flow.locator('#component-client').waitFor();assert.equal(await page.locator('#rp-source').inputValue(),'recorded');assert.equal(await page.locator('.rp-item').filter({hasText:'/new'}).count(),0,'HTTP-only request must not be the default full-flow replay');await page.locator('#rp-source').selectOption('recent');await page.locator('.rp-item').filter({hasText:'/new'}).waitFor();await page.locator('#rp-source').selectOption('recorded');await flow.locator('#component-http').waitFor();
 assert.equal(await modal.evaluate(d=>d.open),false,'HTTP replay starts as drill-down, not root');
 assert.equal(await page.locator('#replay-panel #rp-svg').count(),0,'root Replay must be the full journey');
 assert.equal(new URL(page.url()).pathname,'/','one Replay entry point; no separate journey navigation');
 await flow.locator('#component-http').click();await modal.waitFor({state:'visible'});
 assert.equal(await modal.locator('.rp-stage').count(),7);
 const svg=await modal.locator('#rp-svg').boundingBox(),wrap=await modal.locator('.rp-svgwrap').boundingBox();assert(wrap.height>=svg.height,'stage diagram must not be clipped by its scroll container');
 await modal.getByText('nginx phases inside this stage',{exact:true}).waitFor();
 await modal.getByRole('button',{name:'Next stage',exact:true}).click();
 await modal.locator('#rp-cap').getByText('PRECONTENT',{exact:true}).waitFor();
 await page.locator('#close-deep-replay').click();assert.equal(await modal.evaluate(d=>d.open),false);
 await page.locator('.rp-item').filter({hasText:'/reused'}).click();await flow.getByText('Request on connection 5',{exact:true}).waitFor();
 await flow.getByText('Connection 99',{exact:true}).waitFor();await flow.locator('#open-nginx').click();await modal.waitFor({state:'visible'});
 assert((await page.locator('#deep-replay-request').innerText()).includes('/reused'));
 await page.keyboard.press('Escape');await modal.waitFor({state:'hidden'});
 if(process.env.SCREENSHOT_DIR){fs.mkdirSync(process.env.SCREENSHOT_DIR,{recursive:true});await flow.locator('body').evaluate(()=>window.scrollTo(0,0));await page.screenshot({path:process.env.SCREENSHOT_DIR+'/full-replay.png',fullPage:true});await flow.locator('#open-nginx').click();await modal.waitFor({state:'visible'});await page.screenshot({path:process.env.SCREENSHOT_DIR+'/nginx-drill-down.png'});await page.locator('#close-deep-replay').click();}
 workers={...workers,live_workers:[{worker_pid:11,requests:[{id:'11-22-30',method:'GET',path:'/live-stream',phase:'body_filter_state',io:512,elapsed_ms:15000,response_bytes:20,status:200}]}]};
 await page.locator('#tab-inflight').click();await page.locator('.rp-item').filter({hasText:'/live-stream'}).click();
 await flow.getByText('HTTP elapsed 15000 ms · in flight',{exact:true}).waitFor();await flow.locator('#open-nginx').click();await modal.waitFor({state:'visible'});
 assert((await modal.locator('#rp-cap').innerText()).includes('Last observed hook'));
 await page.goto('http://inspector.test/?replay='+archived.id);await flow.locator('#component-http').click();await modal.waitFor({state:'visible'});assert((await page.locator('#deep-replay-request').innerText()).includes('/archived'),'retained deep links outlive the latest 100 list');
 assert.deepEqual(errors,[]);console.log('PASS: Replay owns full client/kernel/worker flow; nginx drill-down retains seven stages, phase controls, close/Escape, request selection and live state');
}finally{await browser.close();}})().catch(e=>{console.error(e);process.exit(1)});
