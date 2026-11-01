# 广东麻将（推倒胡）后端服务镜像
# 构建：docker build -t gd-mahjong-api .
# 运行：docker run --rm -p 8000:8000 gd-mahjong-api
# syntax=docker/dockerfile:1

FROM python:3.12-slim

# 不写 .pyc、日志实时刷出，方便 docker logs 排查
ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1

WORKDIR /app

# 先只复制依赖清单（pyproject.toml），让「安装依赖」层能命中缓存：
# 只改源码时不会重复下载依赖。
COPY pyproject.toml ./
RUN python -c "import pathlib, tomllib; deps = tomllib.loads(pathlib.Path('pyproject.toml').read_text('utf-8'))['project']['dependencies']; pathlib.Path('/tmp/requirements.txt').write_text('\n'.join(deps) + '\n', 'utf-8')" \
    && pip install --no-cache-dir -r /tmp/requirements.txt \
    && rm -f /tmp/requirements.txt

# 非 root 用户运行，降低容器被打穿后的影响面
RUN useradd --create-home --uid 10001 appuser

# 复制应用代码（.dockerignore 已排除 .venv / __pycache__ / tests / docs 等）
COPY --chown=appuser:appuser app ./app

USER appuser

EXPOSE 8000

# 健康检查直接打 /healthz；用 python 标准库实现，无需在镜像里装 curl
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD python -c "import sys, urllib.request; sys.exit(0 if urllib.request.urlopen('http://127.0.0.1:8000/healthz', timeout=3).status == 200 else 1)"

CMD ["uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8000"]
