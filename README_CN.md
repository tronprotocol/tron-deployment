## 下载/更新启动脚本

由于启动脚本可能会发生变化，请每次使用前都重新下载启动脚本

```shell
wget https://raw.githubusercontent.com/tronprotocol/TronDeployment/master/deploy_tron.sh -O deploy_tron.sh
```

## 启动参数说明

```shell
bash start_tron.sh --app [FullNode|SolidityNode] --net [mainnet|testnet] --db [keep|remove|backup] 

--app	启动的应用，默认FullNode，可以是FullNode或者SolidityNode
--net	连接的网络，默认mainnet，可以是mainnet或者testnet
--db	数据库处理方式，可以keep,remove,backup。如果两次启动不同的网络，需要删除数据库
--trust-node	只有在启动SolidityNode中生效，指定连接的FullNode的gRPC服务 .比如 127.0.0.1:50051 或者13.125.249.129:50051
--rpc-port	grpc的端口号，如果在同一台机器上部署SolidityNode和FullNode，必须配置不同的端口号
--commit	选填，项目commitid
--branch	选填，项目分支
```



## Examples

### 单独启动主网FullNode

```shell
wget https://raw.githubusercontent.com/tronprotocol/TronDeployment/master/deploy_tron.sh -O deploy_tron.sh
bash deploy_tron.sh --app FullNode --net mainnet
```

### 单独启动主网SolidityNode

```shell
wget https://raw.githubusercontent.com/tronprotocol/TronDeployment/master/deploy_tron.sh -O deploy_tron.sh
# 这里自己指定SolidityNode的trust-node的gRPC服务的ip和端口号
bash deploy_tron.sh --app SolidityNode --net mainnet --trust-node <grpc-ip:grpc-port>
```

### 启动主网FullNode和SolidityNode在同一台机器

```shell
wget https://raw.githubusercontent.com/tronprotocol/TronDeployment/master/deploy_tron.sh -O deploy_tron.sh
bash deploy_tron.sh --app FullNode --net mainnet
# 由于FullNode和SolidityNode都需要对外提供gRPC服务
# 所以在同一台机器安装SolidityNode需要配置不同的gRPC端口号
# 不能是默认gRPC端口号50051，在此例中rpc端口设置为50041
sh deploy_tron.sh --app SolidityNode --net mainnet --trust-node 127.0.0.1:50051 --rpc-port 50041
```
