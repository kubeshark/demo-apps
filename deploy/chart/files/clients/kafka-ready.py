import runpy
from pathlib import Path

import confluent_kafka


original_producer = confluent_kafka.Producer


class ReadyProducer:
    def __init__(self, *args, **kwargs):
        self.producer = original_producer(*args, **kwargs)
        self.delivered = 0
        self.failed = False

    def __getattr__(self, name):
        return getattr(self.producer, name)

    def produce(self, *args, **kwargs):
        callback = kwargs.pop("callback", None)

        def delivered(err, msg):
            if err is None:
                self.delivered += 1
            else:
                self.failed = True
            if callback is not None:
                callback(err, msg)

        return self.producer.produce(*args, callback=delivered, **kwargs)

    def flush(self, *args, **kwargs):
        remaining = self.producer.flush(*args, **kwargs)
        if remaining == 0 and self.delivered == 3 and not self.failed:
            Path("/tmp/demo-apps-ready").touch()
        return remaining


confluent_kafka.Producer = ReadyProducer
runpy.run_path("/app/produce.py", run_name="__main__")
