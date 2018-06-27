## 下载/更新启动脚本 download this script in time

because we will update the script frequently, so you need to download the script file in time.

```shell
wget https://raw.githubusercontent.com/tronprotocol/TronDeployment/master/deploy_tron.sh -O deploy_tron.sh
```

## paramter description

```shell
bash start_tron.sh --app [FullNode|SolidityNode] --net [mainnet|testnet] --db [keep|remove|backup] 

--app	FullNode or SolidityNode, default is FullNode
--net	mainnet or testnet, default is mainnet
--db	keep or remove db, default is keep.
--trust-node	only for SolidityNode, specify the fullnode which SolidityNode need to connect to, for example:127.0.0.1:50051
--rpc-port	grpc port，if SolidityNode and FullNode are deployed on a same server，make sure the ports are defferent for SolidityNode and FullNode
--commit	java-tron commitid in github
--branch	java-tron branch in guthub
```



## Examples

### start FullNode in mainnet

```shell
wget https://raw.githubusercontent.com/tronprotocol/TronDeployment/master/deploy_tron.sh -O deploy_tron.sh
bash deploy_tron.sh --app FullNode --net mainnet
```

### start SolidityNode in mainnet

```shell
wget https://raw.githubusercontent.com/tronprotocol/TronDeployment/master/deploy_tron.sh -O deploy_tron.sh
# specify the fullnode ip and port which SolidityNode need to connect in trust-node
bash deploy_tron.sh --app SolidityNode --net mainnet --trust-node <grpc-ip:grpc-port>
```

### deploy FullNode and SolidityNode on a server

```shell
wget https://raw.githubusercontent.com/tronprotocol/TronDeployment/master/deploy_tron.sh -O deploy_tron.sh
bash deploy_tron.sh --app FullNode --net mainnet
# because FullNode and SolidityNode both provide gRPC services, so make sure that the FullNode gRPC port is defferent from SolidityNode gRPC port
# the default of FullNode gRPC port is 50051, so we set SolidityNode gRPC port as 50041 
sh deploy_tron.sh --app SolidityNode --net mainnet --trust-node 127.0.0.1:50051 --rpc-port 50041
```
