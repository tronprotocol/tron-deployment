## Scope of use
This script could be used on Linux/MacOS, but not on Windows.
Just Support FullNode and SolidityNode.

## Download and run script

```shell
wget https://raw.githubusercontent.com/tronprotocol/TronDeployment/master/deploy_tron.sh -O deploy_tron.sh
```

## Parameter Illustration

```shell
bash deploy_tron.sh --app [FullNode|SolidityNode] --net [mainnet|testnet|privatenet] --db [keep|remove|backup] --heap-size <heapsize>

--app	Optional, Running application. The default node is Fullnode and it could be FullNode or SolidityNode.
--net	Optional, Connecting network. The default network is mainnet and it could be mainnet, testnet.
--db	Optional, The way of data processing could be keep, remove and backup. Default is keep. If you launch two different networks, like from mainnet to testnet or from testnet to mainnet, you need to delete database.
--trust-node	Optional, It only works when deploying SolidityNode. Default is 127.0.0.1:50051. The specified gRPC service of Fullnode, like 127.0.0.1:50051 or 13.125.249.129:50051.
--rpc-port	Optional, Port of grpc. Default is 50051. If you deploy SolidityNode and FullNode on the same host，you need to configure different ports.
--commit	Optional, commitid of project.
--branch	Optional, branch of project.  Mainnet default is latest release and Testnet default is master.
--heap-size  Optional, jvm option: Xmx. The default heap-size is 0.8 * memory size.
--work_space  Optional, default is current directory.
```

## Examples

### Deployment of FullNode on the one host.

```shell
wget https://raw.githubusercontent.com/tronprotocol/TronDeployment/master/deploy_tron.sh -O deploy_tron.sh
bash deploy_tron.sh
```

### Deployment of SolidityNode on the one host.

```shell
wget https://raw.githubusercontent.com/tronprotocol/TronDeployment/master/deploy_tron.sh -O deploy_tron.sh
# User can self-configure the IP and Port of GRPC service in the turst-node field of SolidityNode. trust-node is the fullnode you just deploy.
bash deploy_tron.sh --app SolidityNode --trust-node <grpc-ip:grpc-port>
```

### Deployment of FullNode and SolidityNode on the same host.

```shell
# You need to configure different gRPC ports on the same host because gRPC port is available on SolidityNode and FullNodeConfigure and it cannot be set as default value 50051. In this case the default value of rpc port is set as 50041.
wget https://raw.githubusercontent.com/tronprotocol/TronDeployment/master/deploy_tron.sh -O deploy_tron.sh
bash deploy_tron.sh --app FullNode
bash deploy_tron.sh --app SolidityNode --rpc-port 50041
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
