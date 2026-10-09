#!/usr/bin/env python3
"""Real loopback process checks of recovered catalog/badge routes; no external I/O."""
import http.client
import json
import signal
import subprocess
import sys
import tempfile
from pathlib import Path

# Reuse bounded startup/shutdown helpers, overriding only the command readiness.
import queue
import threading
from urllib.parse import urlsplit


def start(binary, config, icons):
    process = subprocess.Popen([binary, '--listen=127.0.0.1:0', '--storage=file', '--data='+str(config.parent/'watch.json'),
                                '--exchange-config='+str(config), '--badge-icons='+str(icons),
                                '--synthetic-wallet-product-catalog', '--wallet-product-config='+str(config)],
                               stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True)
    lines = queue.Queue()
    def drain():
        for line in process.stderr:
            lines.put(line)
        lines.put(None)
    threading.Thread(target=drain, daemon=True).start()
    try:
        for _ in range(10):
            line = lines.get(timeout=10)
            if line is None:
                raise RuntimeError('catalog process stopped before readiness')
            marker = 'Solsynth development service listening at '
            if marker in line:
                address = urlsplit('http://' + line.split(marker, 1)[1].strip())
                if address.hostname != '127.0.0.1' or not address.port:
                    raise RuntimeError('non-loopback or missing bound address')
                return process, address.port
        raise RuntimeError('catalog process missing readiness')
    except BaseException:
        stop(process)
        raise


def stop(process):
    if process.poll() is None:
        process.send_signal(signal.SIGTERM)
        try:
            process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=5)
            raise RuntimeError('catalog process did not stop')
    if process.returncode:
        raise RuntimeError('catalog process failed; logs suppressed')


def request(port, path, method='GET', headers=None):
    connection = http.client.HTTPConnection('127.0.0.1', port, timeout=5)
    try:
        connection.request(method, path, headers=headers or {})
        result = connection.getresponse()
        return result.status, dict(result.getheaders()), result.read()
    finally:
        connection.close()


def check(binary):
    with tempfile.TemporaryDirectory(prefix='solsynth-catalog-') as directory:
        root = Path(directory)
        config = root/'settings.json'
        config.write_text('{"Payment":{"CurrencyExchange":{"1 SYNTH":"3.00 DEMO"},"Product":{"GoldCurrency":{"Currency":"DOC","ProviderMappings":{"Paddle":{"PriceID":"2.50"}}}}}}')
        icons = root/'icons'
        icons.mkdir()
        svg = b'<svg xmlns="http://www.w3.org/2000/svg"><circle r="1"/></svg>'
        (icons/'draw.svg').write_bytes(svg)
        for _ in range(2):
            process, port = start(binary, config, icons)
            try:
                status, _, body = request(port, '/api/wallet-products/catalog')
                assert status == 200 and json.loads(body)['provider_mappings'] == {'paddle':{'price_id':2.5}}
                assert b'2.50' in body and json.loads(body)['currency'] == 'DOC'
                status, _, body = request(port, '/api/wallets/exchange')
                assert status == 200 and json.loads(body) == [{
                    'source_currency':'SYNTH','source_amount':1,
                    'target_currency':'DEMO','target_amount':3}]
                assert b'3.00' in body
                status, headers, body = request(port, '/.well-known/badges')
                data = json.loads(body)
                assert status == 200 and data['version'] == 1 and len(data['badges']) == 23
                assert headers.get('Cache-Control') == 'public,max-age=3600'
                status, headers, body = request(port, '/.well-known/badges/icons/draw')
                assert status == 200 and body == svg and headers.get('Content-Type') == 'image/svg+xml'
                for path in ('/api/wallets/exchange', '/.well-known/badges'):
                    status, headers, body = request(port, path, method='POST')
                    assert status == 405 and headers.get('Allow') == 'GET' and body == b''
                status, _, _ = request(port, '/api/posts/watch', headers={'X-Test-Account':'synthetic-forged'})
                assert status == 401
                assert not (root/'watch.json').exists()
                status, _, _ = request(port, '/api/unimplemented')
                assert status == 404
            finally:
                stop(process)
    print('Verified real catalog/badge HTTP processes, decimal output, protected watch denial and application restarts')

if __name__ == '__main__':
    try:
        check(sys.argv[1])
    except (AssertionError, OSError, RuntimeError, ValueError, queue.Empty) as error:
        sys.exit('Catalog HTTP smoke failed: '+type(error).__name__)
