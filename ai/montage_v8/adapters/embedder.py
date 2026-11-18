"""图文向量模型（Embedder 端口）：EmbeddingGemma 2 经 MediaPipe 编码图片。

MediaPipe 只装在 Linux 的 anas-ai-dev 镜像里，所以模型跑在一个 HTTP 服务里（service/search.py，开发机上在 anas 虚拟机，
板子上直接跑在 Linux 上），这里经 POST /embed 调用。服务给出模型的完整名字（含版本、分词数、是否归一化），写进索引，
和已有索引的模型不同就不混用。
"""

from dataclasses import dataclass, field


@dataclass
class HttpEmbedder:
    url: str
    timeout: int = 120
    _model: str = field(default="", init=False)

    @property
    def model(self) -> str:
        if not self._model:
            import requests

            r = requests.get(f"{self.url}/model", timeout=self.timeout)
            r.raise_for_status()
            self._model = r.json()["model"]
        return self._model

    def embed_image(self, jpeg: bytes) -> list[float]:
        import requests

        r = requests.post(f"{self.url}/embed", data=jpeg, headers={"Content-Type": "image/jpeg"}, timeout=self.timeout)
        if r.status_code != 200:
            raise RuntimeError(f"图片编码服务出错：{r.text[:200]}")
        body = r.json()
        if self._model and body["model"] != self._model:
            raise RuntimeError(f"图片编码服务换了模型：{self._model} → {body['model']}")
        self._model = body["model"]
        return body["vector"]
