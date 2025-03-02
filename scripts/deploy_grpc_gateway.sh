# Prerequests:
# 1. Install go
# 2. Install protoc
# 3. set $GOPATH
# Then run this scripts
#!/bin/bash
port=50051
host=localhost
listen=18890
go get -u github.com/tronprotocol/grpc-gateway
echo "Download github.com/tronprotocol/grpc-gateway successfully"
cd $GOPATH/src/github.com/tronprotocol/grpc-gateway
while [ -n "$1" ] ;do
    case "$1" in
        --rpcport)
            port=$2
            shift 2
            ;;
        --rpchost)
            host=$2
            shift 2
            ;;
        --httpport)
            listen=$2
            shift 2
            ;;
        *)
            ;;
    esac
done
nohup go run tron_http/main.go -port $port -host $host -listen $listen >> start_grpc_gateway.log 2>&1 &
echo "==Activate gateway=="
echo "grpc server : $host:$port"
echo "http port: $listen"