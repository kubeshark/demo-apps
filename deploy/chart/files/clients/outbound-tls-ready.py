import runpy
from pathlib import Path

import requests


ready = Path("/tmp/demo-apps-ready")
ready.unlink(missing_ok=True)
original_request = requests.request


def request(*args, **kwargs):
    response = original_request(*args, **kwargs)
    if 200 <= response.status_code < 300:
        ready.touch()
    return response


requests.request = request
runpy.run_path("/app/example.py", run_name="__main__")
