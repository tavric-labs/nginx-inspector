"""Response equivalence plus bounded, isolated on/off latency measurement."""
import concurrent.futures,hashlib,http.client,json,os,statistics,time,urllib.request
API=os.environ.get('INSPECTOR_URL','http://127.0.0.1:19877')
def capture(active,sample=10000):
 r=urllib.request.Request(API+'/api/capture/'+('start' if active else 'stop'),json.dumps({'seconds':60,'sample_permyriad':sample}).encode() if active else b'{}',{'Content-Type':'application/json'})
 with urllib.request.urlopen(r,timeout=3) as f:return json.load(f)
def request(path,method='GET',data=None,headers=None):
 c=http.client.HTTPConnection('127.0.0.1',18080,timeout=10);started=time.perf_counter_ns();c.request(method,path,body=data,headers=headers or {});r=c.getresponse();body=r.read();elapsed=(time.perf_counter_ns()-started)/1000000
 result=(r.status,sorted((k.lower(),v) for k,v in r.getheaders() if k.lower() not in ('date',)),hashlib.sha256(body).hexdigest());c.close();return result,elapsed
cases=[('/', 'GET',None,{}),('/local','GET',None,{}),('/redirect','GET',None,{}),('/broken','GET',None,{}),('/no-buffer','GET',None,{}),('/file','GET',None,{}),('/compressed','GET',None,{'Accept-Encoding':'gzip'}),('/upload?token=fixture','POST',b'unchanged-body',{'Content-Type':'application/octet-stream'})]
try:
 capture(False);baseline=[request(*case)[0] for case in cases]
 capture(True);observed=[request(*case)[0] for case in cases]
 assert baseline==observed,'capture changed HTTP status, headers or body'
 print('PASS: capture OFF/ON preserves HTTP status, headers and body for proxy, local response, rewrite, error, buffering override, file, gzip and upload')
 if os.environ.get('INSPECTOR_BENCHMARK')=='1':
  rounds=[]
  for sample in [0,100,10000,10000,100,0,0,100,10000]:
   active=sample>0
   capture(active,sample)
   time.sleep(.75)
   with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
    for _ in pool.map(lambda _:request('/benchmark-warm'),range(64)):pass
    started=time.perf_counter();timings=[v[1] for v in pool.map(lambda _:request('/benchmark-fixture'),range(2048))];elapsed=time.perf_counter()-started
   timings.sort();rounds.append({'capture':active,'sample_permyriad':sample,'requests':2048,'rps':2048/elapsed,'p50_ms':statistics.median(timings),'p95_ms':timings[int(len(timings)*.95)-1]})
  off=[v for v in rounds if not v['capture']]
  comparisons=[]
  for sample in [100,10000]:
   on=[v for v in rounds if v['sample_permyriad']==sample]
   comparisons.append({'sample_permyriad':sample,'median_p95_delta_ms':statistics.median(v['p95_ms'] for v in on)-statistics.median(v['p95_ms'] for v in off),'median_throughput_change_percent':100*(statistics.median(v['rps'] for v in on)/statistics.median(v['rps'] for v in off)-1)})
  result={'scope':'isolated fixture, concurrency 8; not a production SLO','rounds':rounds,'comparisons':comparisons}
  print(json.dumps(result))
finally:capture(False)
