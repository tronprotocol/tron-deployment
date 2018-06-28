# Download and run script

```shell
wget https://raw.githubusercontent.com/tronprotocol/TronDeployment/master/deploy_tron.sh -O deploy_tron.sh
```

# Parameter Illustration

```shell
bash start_tron.sh --app [FullNode|SolidityNode] --net [mainnet|testnet] --db [keep|remove|backup] 

--app	Running application. The default node is Fullnode and it could be FullNode or SolidityNode.
--net	Connecting network. The default network is mainnet and it could be mainnet or testnet.
--db	The way of data processing could be keep, remove and backup. If you launch two different networks, like from mainnet to testnet or from testnet to mainnet, you need to delete database. 
--trust-node	It only works when deploying SolidityNode. The specified gRPC service of Fullnode, like 127.0.0.1:50051 or 13.125.249.129:50051.
--rpc-port	Port of grp. If you deploy SolidityNode and FullNode on the same host，you need to configure different ports.
--commit	Optional， commitid of project.
--branch	Optional，branch of project.
```

## Examples

### Deployment of FullNode on the one host.

```shell
wget https://raw.githubusercontent.com/tronprotocol/TronDeployment/master/deploy_tron.sh -O deploy_tron.sh
bash deploy_tron.sh --app FullNode --net mainnet
```

### Deployment of SolidityNode on the one host.

```shell
wget https://raw.githubusercontent.com/tronprotocol/TronDeployment/master/deploy_tron.sh -O deploy_tron.sh
# User can self-configure the IP and Port of GRPC service in the turst-node field of SolidityNode.
bash deploy_tron.sh --app SolidityNode --net mainnet --trust-node <grpc-ip:grpc-port>
```

### Deployment of FullNode and SolidityNode on the same host.

```shell
wget https://raw.githubusercontent.com/tronprotocol/TronDeployment/master/deploy_tron.sh -O deploy_tron.sh
bash deploy_tron.sh --app FullNode --net mainnet
# You need to configure different gRPC ports on the same host because gRPC port is available on SolidityNode and FullNodeConfigure and it cannot be set as default value 50051. In this case the default value of rpc port is set as 50041. 
sh deploy_tron.sh --app SolidityNode --net mainnet --trust-node 127.0.0.1:50051 --rpc-port 50041
```
