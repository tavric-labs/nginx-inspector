const assert=require('node:assert/strict'),fs=require('node:fs');const {chromium}=require('playwright');
(async()=>{const browser=await chromium.launch({headless:true,args:['--no-sandbox']});try{
 const page=await browser.newPage({viewport:{width:1600,height:1200}}),errors=[];page.on('pageerror',e=>errors.push(e.message));
 const id='11-1-2-1000',base=90071992547409930n;
 const rec=(event,offset,extra={})=>({schema:1,kind:'transport',session:'a'.repeat(32),master_pid:10,host_pid:11,fd:9,generation:String(base),at_ns:String(base+BigInt(offset)),event,...extra});
 const begin=rec('request_begin',5000000,{request_id:id,request_number:1}),end=rec('request_end',20000000,{request_id:id,request_number:1});
 const e={schema:2,id,method:'GET',path:'/inspect?<img src=x onerror=alert(1)>',status:200,duration_ms:20,native:{nginx:{worker_pid:11},connection:{id:2,request_number:1,protocol:'HTTP/1.1'},trace:[{event:'post_read',at_ms:0}]}};
 let view={request_id:id,status:'correlated_connection',connection_shared:true,truncated:false,begin,end,records:[rec('accept',1000000,{syn_ns:String(base-2000000n),queued_ns:String(base-1000000n)}),rec('register',2000000,{flags:8193}),rec('ready',3000000,{flags:1}),rec('read',4000000,{bytes:50,duration_ns:'100000'}),begin,rec('request_begin',6000000,{request_id:'11-2-2-1001',request_number:2}),end]};
 await page.route('http://inspector.test/**',route=>{const path=new URL(route.request().url()).pathname;let body,contentType='application/json';if(path==='/journey'){body=fs.readFileSync('collector/journey.html','utf8');contentType='text/html';}else body=JSON.stringify(path==='/api/events'?[e]:path.startsWith('/api/transport/')?view:e);return route.fulfill({contentType,body});});
 await page.goto('http://inspector.test/journey?id='+id);await page.getByText(/Correlated capture: the native request marker/).waitFor();
 assert.equal(await page.locator('#events tr').count(),9,'queue hook points must use their captured timestamps');
 assert((await page.locator('#metrics').innerText()).includes('1 ms'),'64-bit nanoseconds must not be rounded before subtraction');
 assert((await page.locator('#events').innerText()).includes('-7 ms'),'connection setup precedes selected request marker');
 assert.equal(await page.locator('#component-rx').getAttribute('class'),'component','missing NIC events remain a mechanism');
 assert((await page.locator('#component-syn').getAttribute('class')).includes('observed'));
 assert.equal(await page.locator('img').count(),0,'request values are text, not markup');
 await page.locator('#next').click();assert.equal(await page.locator('#position').innerText().then(t=>t.includes('Accept-queue hook')),true);
 await page.locator('#next').click();await page.locator('#next').click();assert((await page.locator('#event-detail').innerText()).includes('EPOLLIN'));await page.locator('#play').click();await page.waitForTimeout(900);assert((await page.locator('#event-detail').innerText()).includes('epoll returned'));await page.locator('#play').click();
 await page.locator('#component-loop').click();assert((await page.locator('#intent').innerText()).includes('not automatically scheduler queue delay'));
 if(process.env.SCREENSHOT_DIR){fs.mkdirSync(process.env.SCREENSHOT_DIR,{recursive:true});await page.screenshot({path:process.env.SCREENSHOT_DIR+'/request-journey.png',fullPage:true});}
 view={request_id:id,status:'ambiguous',records:[]};await page.locator('#refresh').click();await page.getByText(/Conflicting transport correlations/).waitFor();assert.equal(await page.locator('#puck').getAttribute('visibility'),'hidden');
 view={request_id:id,status:'not_captured',records:[]};await page.locator('#refresh').click();await page.getByText(/No correlated transport capture for this request/).waitFor();assert.equal(await page.locator('#play').isDisabled(),true);
 assert.deepEqual(errors,[]);console.log('PASS: measured queue points, exact nanosecond deltas, shared scopes, unknown nodes, XSS safety, ambiguity and missing capture');
}finally{await browser.close();}})().catch(e=>{console.error(e);process.exit(1)});
