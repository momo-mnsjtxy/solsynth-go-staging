#!/usr/bin/env python3
"""Final disposable PG smoke: real package/NodeInfo processes, no redirect following."""
import http.client
import importlib.util
import json
import os
import queue
import subprocess
import sys
import tempfile
import threading
from pathlib import Path
from urllib.parse import urlsplit

spec = importlib.util.spec_from_file_location('catalog_smoke', Path(__file__).with_name('verify-catalog-http.py'))
helper = importlib.util.module_from_spec(spec)
spec.loader.exec_module(helper)


def validate_fixture():
    run = os.environ.get('SOLSYNTH_DISPOSABLE_PG', '')
    expected = f'host={run}/socket port=55432 user=synthetic dbname=postgres sslmode=disable'
    if (not run or not Path(run).is_absolute() or not (Path(run)/'.solsynth-disposable').is_file()
            or os.environ.get('PG_TEST_REQUIRED') != '1' or os.environ.get('PG_TEST_DSN') != expected):
        raise RuntimeError('marked disposable private-socket database required')
    return expected


def start(binary, args, marker, env):
    process = subprocess.Popen([binary,'--listen=127.0.0.1:0']+args,
                               stdout=subprocess.DEVNULL,stderr=subprocess.PIPE,text=True,env=env)
    lines = queue.Queue()
    def drain():
        for line in process.stderr:
            lines.put(line)
        lines.put(None)
    threading.Thread(target=drain,daemon=True).start()
    try:
        for _ in range(10):
            line=lines.get(timeout=10)
            if line is None:
                raise RuntimeError('candidate exited before listening')
            if marker in line:
                address=urlsplit('http://'+line.split(marker,1)[1].strip())
                if address.hostname!='127.0.0.1' or not address.port:
                    raise RuntimeError('invalid listen address')
                return process,address.port
        raise RuntimeError('candidate missing readiness')
    except BaseException:
        helper.stop(process)
        raise


def read_counter(psql,dsn):
    result=subprocess.run([psql,dsn,'-A','-t','-v','ON_ERROR_STOP=1','-c',
        "SELECT download_count FROM mini_apps WHERE id='a8000000-0000-0000-0000-00000000000a'"],
        stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,text=True,check=True)
    return int(result.stdout.strip())


def check(develop,nodeinfo,psql):
    dsn=validate_fixture()
    environment=dict(os.environ)
    before=read_counter(psql,dsn)
    with tempfile.TemporaryDirectory(prefix='solsynth-package-smoke-') as directory:
        args=['--synthetic-production-status=42','--postgres-dsn-env=PG_TEST_DSN',
              '--synthetic-miniapp-stage=71','--synthetic-package-directory='+directory,'--synthetic-bot-commands']
        for _ in range(2):
            process,port=start(develop,args,'Develop discovery listening on ',environment)
            try:
                status,_,body=helper.request(port,'/api/bots/ab000000-0000-0000-0000-00000000000a/commands')
                assert status==200 and json.loads(body)==[{'name':'persist','description':'Persisted command','usage':None,'parameters':[]}]
                status,headers,body=helper.request(port,'/api/miniapps/package-restart/package')
                assert status==302 and headers.get('Location')=='https://packages.example.invalid/restart.zip'
                status,_,_=helper.request(port,'/api/miniapps/package-restart/package',headers={'Authorization':'Bearer synthetic-invalid'})
                assert status==403
                status,_,_=helper.request(port,'/api/miniapps/absent/package')
                assert status==404
            finally:
                helper.stop(process)
    assert read_counter(psql,dsn)==before+2
    environment['NODEINFO_SYNTHETIC_DSN']=dsn+' search_path=nodeinfo_candidate'
    args=['--synthetic-fediverse-type=73','--synthetic-soft-delete-policy=deleted-at-is-null',
          '--postgres-dsn-env=NODEINFO_SYNTHETIC_DSN','--activitypub-domain=synthetic.invalid']
    for _ in range(2):
        process,port=start(nodeinfo,args,'NodeInfo listening on ',environment)
        try:
            status,headers,body=helper.request(port,'/.well-known/host-meta')
            assert status==200 and headers.get('Content-Type')=='application/xrd+xml; charset=utf-8'
            assert b'https://synthetic.invalid/.well-known/webfinger?resource={uri}' in body
            status,_,body=helper.request(port,'/.well-known/nodeinfo')
            links=json.loads(body)['links']
            assert status==200 and links[0]['href']=='https://synthetic.invalid/nodeinfo/2.1'
            status,_,body=helper.request(port,'/.well-known/nodeinfo/2.1')
            data=json.loads(body)
            assert status==200 and data['version']=='2.1'
            assert data['usage']['users']=={'total':1,'active_month':1,'active_halfyear':1}
            assert data['usage']['local_posts']==1
            status,_,_=helper.request(port,'/.well-known/nodeinfo/2.1',headers={'Cookie':'synthetic-invalid'})
            assert status==403
        finally:
            helper.stop(process)
    print('Verified real package redirects/counter-backed process and NodeInfo DB statistics across application restarts')

if __name__=='__main__':
    try:
        check(*sys.argv[1:])
    except (AssertionError,OSError,RuntimeError,ValueError,queue.Empty,KeyError,subprocess.CalledProcessError) as error:
        sys.exit('Candidate HTTP smoke failed: '+type(error).__name__)
