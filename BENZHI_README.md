# BENZHI_README

这是一个基于 Go 实现的后端服务，用于承载 go-label-vesseltrial-g13-v1 的业务处理、数据管理与稳定运行。

## 标准构建、运行和测试命令

进入容器后执行：

```bash
# 编译
cd '/app' && GOTOOLCHAIN=local go build ./...

# 启动
cd '/app' && GOTOOLCHAIN=local go run ./cmd/server

# 测试
cd '/app' && GOTOOLCHAIN=local go test ./...
```

## Docker 构建和进入容器

```bash
chmod +x build_benzhi_docker.sh
./build_benzhi_docker.sh benzhi-task-410-amd64 linux/amd64
./build_benzhi_docker.sh benzhi-task-410-arm64 linux/arm64
docker run -it benzhi-task-410-amd64:latest
docker run -it --platform linux/arm64 benzhi-task-410-arm64:latest
```
