FROM golang:1.25 AS builder

# 缓存依赖
COPY go.sum /sealdice/go.sum
COPY go.mod /sealdice/go.mod
# 设置工作路径，下载依赖
WORKDIR /sealdice
RUN go mod download
RUN go install github.com/pointlander/peg@v1.0.1

# 复制代码进行编译
COPY . /sealdice
RUN go generate ./...
RUN go build


FROM golang:1.25

VOLUME [ "/sealdice/data", "/sealdice/backups" ]
EXPOSE 3211

RUN apt-get update && apt-get install -y tzdata

RUN ln -fs /usr/share/zoneinfo/Asia/Shanghai /etc/localtime
RUN dpkg-reconfigure -f noninteractive tzdata

# RUN pip3 install pillow requests chinesecalendar jmcomic 


ENV DB_TYPE=sqlite

COPY --chmod=0755 --from=builder /sealdice/sealdice-core /sealdice/sealdice-core

WORKDIR /sealdice
# CMD ["tail", "-f", "/dev/null"]
CMD [ "/sealdice/sealdice-core", "--container-mode" ]
