import http.server
import time


class Backend(http.server.BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'

    def do_GET(self):
        if self.path.startswith('/slow'):
            time.sleep(.05)
        body = b'hello from test backend\n'
        self.send_response(503 if self.path.startswith('/error') else 200)
        self.send_header('Content-Type', 'text/plain')
        self.send_header('Content-Length', str(len(body)))
        self.send_header('Set-Cookie', 'test-secret=not-to-be-captured')
        self.end_headers()
        if self.command != 'HEAD':
            self.wfile.write(body)

    do_POST = do_GET
    do_HEAD = do_GET

    def log_message(self, *args):
        pass


http.server.ThreadingHTTPServer(('127.0.0.1', 18081), Backend).serve_forever()
