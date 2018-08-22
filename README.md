## Download and run script

```shell
wget https://raw.githubusercontent.com/tronprotocol/TronDeployment/master/deploy_tron.sh -O deploy_tron.sh
```

## Parameter Illustration

```shell
bash deploy_tron.sh --app [FullNode|SolidityNode] --net [mainnet|testnet|privatenet] --db [keep|remove|backup] 

--app	Running application. The default node is Fullnode and it could be FullNode or SolidityNode.
--net	Connecting network. The default network is mainnet and it could be mainnet, testnet, privatenet .
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

### Deployment of FullNode(PrivateNet: Just one witness node) on the one host.

```shell
wget https://raw.githubusercontent.com/tronprotocol/TronDeployment/master/deploy_tron.sh -O deploy_tron.sh
bash deploy_tron.sh --app FullNode --branch develop --net privatenet
```
### Deployment of FullNode and SolidityNode on the same host.

```shell
wget https://raw.githubusercontent.com/tronprotocol/TronDeployment/master/deploy_tron.sh -O deploy_tron.sh
bash deploy_tron.sh --app FullNode --net mainnet
# You need to configure different gRPC ports on the same host because gRPC port is available on SolidityNode and FullNodeConfigure and it cannot be set as default value 50051. In this case the default value of rpc port is set as 50041. 
bash deploy_tron.sh --app SolidityNode --net mainnet --trust-node 127.0.0.1:50051 --rpc-port 50041
```

## Deployment of grpc gateway

### Summary
This script helps you download the code from https://github.com/tronprotocol/grpc-gateway and deploy the code on your environment.
### Pre-requests
Please follow the guide on https://github.com/tronprotocol/grpc-gateway 
Install Golang, Protoc, and set $GOPATH environment variable according to your requirement.
### Download and run script
```shell
wget https://raw.githubusercontent.com/tronprotocol/TronDeployment/master/deploy_grpc_gateway.sh -O deploy_grpc_gateway.sh
```
### Parameter Illustration
```shell
bash deploy_grpc_gateway.sh --rpchost [rpc host ip] --rpcport [rpc port number] --httpport [http port number] 

--rpchost The fullnode or soliditynode IP where the grpc service is provided. Default value is "localhost".
--rpcport The fullnode or soliditynode port number grpc service is consuming. Default value is 50051.
--httpport The port intends to provide http service provided by grpc gateway. Default value is 18890.
```
### Example
Use default configuration：
```shell
bash deploy_grpc_gateway.sh
```
Use customized configuration：
```shell
bash deploy_grpc_gateway.sh --rpchost 127.0.0.1 --rpcport 50052 --httpport 18891
```
