const assert=require('node:assert/strict');
const fs=require('node:fs');
const {chromium}=require('playwright');
(async()=>{
 const browser=await chromium.launch({headless:true,args:['--no-sandbox']});
 const page=await browser.newPage({viewport:{width:1600,height:1100}}),errors=[];
 page.on('pageerror',e=>errors.push(e.message));
 const now=Date.now();
 function event(path,number,protocol){return {schema:2,id:'11-22-'+number,started_ms:now,duration_ms:8,method:'GET',path,status:200,response_body_bytes:20,phases_ms:{},upstream:{},native:{nginx:{worker_pid:11,event_backend:'epoll'},connection:{id:99,request_number:number,protocol},trace:[{event:'post_read',at_ms:0,subrequest:0},{event:'precontent',at_ms:2,subrequest:0},{event:'header_filter',at_ms:3,subrequest:0},{event:'log',at_ms:8,subrequest:0}],attempts:[],subrequests:[],policy:{},request_body:{}}};}
 const events=[event('/new',1,'HTTP/1.1'),event('/reused',5,'HTTP/1.1'),event('/multiplexed',9,'HTTP/2.0'),event('/other-backend',12,'HTTP/1.1')];events[3].native.nginx.event_backend='kqueue';
 let workers={available:false,capture_active:true,processes:[],live_workers:[]};
 await page.route('http://inspector.test/**',async route=>{
  const path=new URL(route.request().url()).pathname;
  const data={'/api/events':events,'/api/summary':{retained:4,window:4,errors:0},'/api/capture':{active:true,sample_permyriad:10000,remaining_seconds:60},'/api/workers':workers};
  await route.fulfill({contentType:path==='/'?'text/html':'application/json',body:path==='/'?fs.readFileSync('collector/viewer.html','utf8'):JSON.stringify(data[path]??{})});
 });
 await page.goto('http://inspector.test/');await page.locator('#show-replay').click();
 const kernel=page.locator('#rp-kernel-path');await kernel.getByText('Kernel → socket → nginx worker',{exact:true}).waitFor();
 assert.equal(await kernel.locator('.kernel-steps button').count(),8);
 assert.equal(await page.locator('#rp-svg .rp-stage').count(),7,'HTTP stages must remain unchanged');
 assert((await kernel.innerText()).includes('Per-request kernel and queue timings are not measured'));
 assert((await kernel.innerText()).includes('Observed +0 ms from nginx request start'));
 await kernel.getByRole('button',{name:'Next transport step',exact:true}).click();
 assert((await kernel.locator('.kernel-explanation').innerText()).includes('SYN queue'));
 const prior=await kernel.locator('.kernel-steps [aria-pressed=true]').innerText();
 await page.getByRole('button',{name:'Next stage',exact:true}).click();
 assert.equal(await kernel.locator('.kernel-steps [aria-pressed=true]').innerText(),prior,'transport stepping is independent of HTTP playback');
 await page.locator('.rp-item').filter({hasText:'/reused'}).click();
 await kernel.getByText(/Connection reuse observed/).waitFor();
 assert.equal(await kernel.locator('.kernel-steps .earlier').count(),4);
 assert((await kernel.innerText()).includes('Earlier connection setup · duration unknown'));
 await page.locator('.rp-item').filter({hasText:'/multiplexed'}).click();
 await kernel.getByText(/HTTP\/2: socket readiness is connection-level/).waitFor();
 await page.locator('.rp-item').filter({hasText:'/other-backend'}).click();
 await kernel.getByText(/Applicability: this record reports/).waitFor();
 workers={...workers,live_workers:[{worker_pid:11,requests:[{id:'11-22-30',method:'GET',path:'/live-stream',phase:'body_filter_state',io:512,elapsed_ms:15000,response_bytes:20,status:200}]}]};
 await page.locator('#tab-inflight').click();await page.locator('.rp-item').filter({hasText:'/live-stream'}).click();
 await kernel.getByText(/Connection setup applicability is unknown/).waitFor();
 assert((await kernel.innerText()).includes('Still in flight · no final duration'));
 assert((await kernel.innerText()).includes('POST_READ hook offset'));
 assert.deepEqual(errors,[]);
 if(process.env.SCREENSHOT_DIR){await fs.promises.mkdir(process.env.SCREENSHOT_DIR,{recursive:true});await page.locator('#tab-completed').click();await page.locator('.rp-item').filter({hasText:'/reused'}).click();await kernel.screenshot({path:process.env.SCREENSHOT_DIR+'/kernel-to-worker.png'});}
 await browser.close();console.log('PASS: independent transport walkthrough, unknown timings, reused connections, HTTP/2, other backends, live requests and preserved HTTP stages');
})().catch(e=>{console.error(e);process.exit(1)});
