# build on the native platform and cross-compile
FROM --platform=$BUILDPLATFORM martenseemann/quic-network-simulator-endpoint:latest AS builder

ARG BUILDPLATFORM
ARG TARGETPLATFORM
ARG TARGETOS
ARG TARGETARCH
RUN echo "BUILDPLATFORM: ${BUILDPLATFORM}, TARGETPLATFORM: ${TARGETPLATFORM}"

RUN apt-get update && apt-get install -y wget tar git && rm -rf /var/lib/apt/lists/*

ENV GOVERSION=1.27.0

RUN platform=$(echo ${BUILDPLATFORM} | tr '/' '-') && \
  filename="go${GOVERSION}.${platform}.tar.gz" && \
  wget https://dl.google.com/go/${filename} && \
  tar xfz ${filename} && \
  rm ${filename}

ENV PATH="/go/bin:${PATH}"
ENV GOOS=${TARGETOS} GOARCH=${TARGETARCH} CGO_ENABLED=0

# build with --build-arg CACHEBUST=$(date +%s)
ARG CACHEBUST=1

WORKDIR /webtransport-go

COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .

RUN git rev-parse HEAD | tee commit.txt
RUN go build -o server interop/server/main.go
RUN go build -o client interop/client/main.go


FROM martenseemann/quic-network-simulator-endpoint:latest

WORKDIR /webtransport-go

COPY --from=builder /webtransport-go/commit.txt /webtransport-go/server /webtransport-go/client ./
COPY --from=builder --chmod=755 /webtransport-go/interop/run_endpoint.sh ./

ENTRYPOINT [ "./run_endpoint.sh" ]
