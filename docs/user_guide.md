# User Guide

Welcome to the User Guide for the repository. This guide provides detailed instructions on how to use the repository, including setup, configuration, and troubleshooting.

## Table of Contents

1. [Introduction](#introduction)
2. [Setup](#setup)
3. [Configuration](#configuration)
4. [Usage](#usage)
5. [Troubleshooting](#troubleshooting)
6. [Additional Resources](#additional-resources)

## Introduction

This repository provides scripts and configuration files to help you deploy and manage a TRON network node. The scripts support both FullNode and SolidityNode deployments and include options for customizing the deployment based on your requirements.

## Setup

### Prerequisites

Before you begin, ensure that you have the following dependencies installed on your system:

- `java` (Oracle JDK, version >= 1.8)
- `git`
- `wget`
- `go` (for deploying the gRPC gateway)
- `protoc` (for deploying the gRPC gateway)

### Downloading the Scripts

To download the deployment scripts, run the following command:

```shell
wget https://raw.githubusercontent.com/tronprotocol/TronDeployment/master/deploy_tron.sh -O deploy_tron.sh
wget https://raw.githubusercontent.com/tronprotocol/TronDeployment/master/deploy_grpc_gateway.sh -O deploy_grpc_gateway.sh
```

## Configuration

The repository includes several configuration files to help you set up and customize your deployment. These files are located in the `config` directory:

- `main_net_config.conf`: Configuration file for the main network.
- `private_net_config.conf`: Configuration file for a private network.
- `test_net_config.conf`: Configuration file for the test network.

### Example Configuration

Here is an example of a configuration file (`main_net_config.conf`):

```conf
net {
  type = mainnet
  # type = testnet
}

storage {
  # Directory for storing persistent data
  db.engine = "LEVELDB",
  db.sync = false,
  db.directory = "database",
  index.directory = "index",
  transHistory.switch = "on",
  # You can custom these 14 databases' configs:

  # account, account-index, asset-issue, block, block-index,
  # block_KDB, peers, properties, recent-block, trans,
  # utxo, votes, witness, witness_schedule.

  # Otherwise, db configs will remain default and data will be stored in
  # the path of "output-directory" or which is set by "-d" ("--output-directory").
  # setting can impove leveldb performance .... start
  # node: if this will increase process fds,you may be check your ulimit if 'too many open files' error occurs
  # see https://github.com/tronprotocol/tips/blob/master/tip-343.md for detail
  # if you find block sync has lower performance,you can try  this  settings
  #default = {
  #  maxOpenFiles = 100
  #}
  #defaultM = {
  #  maxOpenFiles = 500
  #}
  #defaultL = {
  #  maxOpenFiles = 1000
  #}
  # setting can impove leveldb performance .... end
  # Attention: name is a required field that must be set !!!
  properties = [
    //    {
    //      name = "account",
    //      path = "storage_directory_test",
    //      createIfMissing = true,
    //      paranoidChecks = true,
    //      verifyChecksums = true,
    //      compressionType = 1,        // compressed with snappy
    //      blockSize = 4096,           // 4  KB =         4 * 1024 B
    //      writeBufferSize = 10485760, // 10 MB = 10 * 1024 * 1024 B
    //      cacheSize = 10485760,       // 10 MB = 10 * 1024 * 1024 B
    //      maxOpenFiles = 100
    //    },
    //    {
    //      name = "account-index",
    //      path = "storage_directory_test",
    //      createIfMissing = true,
    //      paranoidChecks = true,
    //      verifyChecksums = true,
    //      compressionType = 1,        // compressed with snappy
    //      blockSize = 4096,           // 4  KB =         4 * 1024 B
    //      writeBufferSize = 10485760, // 10 MB = 10 * 1024 * 1024 B
    //      cacheSize = 10485760,       // 10 MB = 10 * 1024 * 1024 B
    //      maxOpenFiles = 100
    //    },
  ]

  needToUpdateAsset = true

  //dbsettings is needed when using rocksdb as the storage implement (db.engine="ROCKSDB").
  //we'd strongly recommend that do not modify it unless you know every item's meaning clearly.
  dbSettings = {
    levelNumber = 7
    //compactThreads = 32
    blocksize = 64  // n * KB
    maxBytesForLevelBase = 256  // n * MB
    maxBytesForLevelMultiplier = 10
    level0FileNumCompactionTrigger = 4
    targetFileSizeBase = 256  // n * MB
    targetFileSizeMultiplier = 1
  }

  //backup settings when using rocks db as the storage implement (db.engine="ROCKSDB").
  //if you want to use the backup plugin, please confirm set the db.engine="ROCKSDB" above.
  backup = {
    enable = false  // indicate whether enable the backup plugin
    propPath = "prop.properties" // record which bak directory is valid
    bak1path = "bak1/database" // you must set two backup directories to prevent application halt unexpected(e.g. kill -9).
    bak2path = "bak2/database"
    frequency = 10000   // indicate backup db once every 10000 blocks processed.
  }

  balance.history.lookup = false

  # checkpoint.version = 2
  # checkpoint.sync = true

  # the estimated number of block transactions (default 1000, min 100, max 10000).
  # so the total number of cached transactions is 65536 * txCache.estimatedTransactions
  # txCache.estimatedTransactions = 1000

  # data root setting, for check data, currently, only reward-vi is used.

  # merkleRoot = {
  # reward-vi = 9debcb9924055500aaae98cdee10501c5c39d4daa75800a996f4bdda73dbccd8 // main-net, Sha256Hash, hexString
  # }
}

node.discovery = {
  enable = true
  persist = true
}

# custom stop condition
#node.shutdown = {
#  BlockTime  = "54 59 08 * * ?" # if block header time in persistent db matched.
#  BlockHeight = 33350800 # if block header height in persistent db matched.
#  BlockCount = 12 # block sync count after node start.
#}

node.backup {
  # udp listen port, each member should have the same configuration
  port = 10001

  # my priority, each member should use different priority
  priority = 8

  # time interval to send keepAlive message, each member should have the same configuration
  keepAliveInterval = 3000

  # peer's ip list, can't contain mine
  members = [
    # "ip",
    # "ip"
  ]
}

crypto {
  engine = "eckey"
}
# prometheus metrics start
# node.metrics = {
#  prometheus{
#    enable=true
#    port="9527"
#  }
# }

# prometheus metrics end

node {
  # trust node for solidity node
  # trustNode = "ip:port"
  trustNode = "127.0.0.1:50051"

  # expose extension api to public or not
  walletExtensionApi = true

  listen.port = 18888

  connection.timeout = 2

  fetchBlock.timeout = 200

  tcpNettyWorkThreadNum = 0

  udpNettyWorkThreadNum = 1

  # Number of validate sign thread, default availableProcessors / 2
  # validateSignThreadNum = 16

  maxConnections = 30

  minConnections = 8

  minActiveConnections = 3

  maxConnectionsWithSameIp = 2

  maxHttpConnectNumber = 50

  minParticipationRate = 15

  isOpenFullTcpDisconnect = false

  p2p {
    version = 11111 # 11111: mainnet; 20180622: testnet
  }

  active = [
    # Active establish connection in any case
    # Sample entries:
    # "ip:port",
    # "ip:port"
  ]

  passive = [
    # Passive accept connection in any case
    # Sample entries:
    # "ip:port",
    # "ip:port"
  ]

  fastForward = [
    "100.26.245.209:18888",
    "15.188.6.125:18888"
  ]

  http {
    fullNodeEnable = true
    fullNodePort = 8090
    solidityEnable = true
    solidityPort = 8091
  }

  rpc {
    port = 50051
    #solidityPort = 50061
    # Number of gRPC thread, default availableProcessors / 2
    # thread = 16

    # The maximum number of concurrent calls permitted for each incoming connection
    # maxConcurrentCallsPerConnection =

    # The HTTP/2 flow control window, default 1MB
    # flowControlWindow =

    # Connection being idle for longer than which will be gracefully terminated
    maxConnectionIdleInMillis = 60000

    # Connection lasting longer than which will be gracefully terminated
    # maxConnectionAgeInMillis =

    # The maximum message size allowed to be received on the server, default 4MB
    # maxMessageSize =

    # The maximum size of header list allowed to be received, default 8192
    # maxHeaderListSize =

    # Transactions can only be broadcast if the number of effective connections is reached.
    minEffectiveConnection = 1
   
    # The switch of the reflection service, effective for all gRPC services
    # reflectionService = true
  }

  # number of solidity thread in the FullNode.
  # If accessing solidity rpc and http interface timeout, could increase the number of threads,
  # The default value is the number of cpu cores of the machine.
  #solidity.threads = 8

  # Limits the maximum percentage (default 75%) of producing block interval
  # to provide sufficient time to perform other operations e.g. broadcast block
  # blockProducedTimeOut = 75

  # Limits the maximum number (default 700) of transaction from network layer
  # netMaxTrxPerSecond = 700

  # Whether to enable the node detection function, default false
  # nodeDetectEnable = false

  # use your ipv6 address for node discovery and tcp connection, default false
  # enableIpv6 = false

  # if your node's highest block num is below than all your pees', try to acquire new connection. default false
  # effectiveCheckEnable = false

  # Dynamic loading configuration function, disabled by default
  # dynamicConfig = {
    # enable = false
    # Configuration file change check interval, default is 600 seconds
    # checkInterval = 600
  # }

  dns {
    # dns urls to get nodes, url format tree://{pubkey}@{domain}, default empty
    treeUrls = [
      #"tree://AKMQMNAJJBL73LXWPXDI4I5ZWWIZ4AWO34DWQ636QOBBXNFXH3LQS@main.trondisco.net", //offical dns tree
    ]

    # enable or disable dns publish, default false
    # publish = false

    # dns domain to publish nodes, required if publish is true
    # dnsDomain = "nodes1.example.org"

    # dns private key used to publish, required if publish is true, hex string of length 64
    # dnsPrivate = "b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291"

    # known dns urls to publish if publish is true, url format tree://{pubkey}@{domain}, default empty
    # knownUrls = [
    #"tree://APFGGTFOBVE2ZNAB3CSMNNX6RRK3ODIRLP2AA5U4YFAA6MSYZUYTQ@nodes2.example.org",
    # ]

    # staticNodes = [
    # static nodes to published on dns
    # Sample entries:
    # "ip:port",
    # "ip:port"
    # ]

    # merge several nodes into a leaf of tree, should be 1~5
    # maxMergeSize = 5

    # only nodes change percent is bigger then the threshold, we update data on dns
    # changeThreshold = 0.1

    # dns server to publish, required if publish is true, only aws or aliyun is support
    # serverType = "aws"

    # access key id of aws or aliyun api, required if publish is true, string
    # accessKeyId = "your-key-id"

    # access key secret of aws or aliyun api, required if publish is true, string
    # accessKeySecret = "your-key-secret"

    # if publish is true and serverType is aliyun, it's endpoint of aws dns server, string
    # aliyunDnsEndpoint = "alidns.aliyuncs.com"

    # if publish is true and serverType is aws, it's region of aws api, such as "eu-south-1", string
    # awsRegion = "us-east-1"

    # if publish is true and server-type is aws, it's host zone id of aws's domain, string
    # awsHostZoneId = "your-host-zone-id"
  }

  # open the history query APIs(http&GRPC) when node is a lite fullNode,
  # like {getBlockByNum, getBlockByID, getTransactionByID...}.
  # default: false.
  # note: above APIs may return null even if blocks and transactions actually are on the blockchain
  # when opening on a lite fullnode. only open it if the consequences being clearly known
  # openHistoryQueryWhenLiteFN = false

  jsonrpc {
    # Note: If you turn on jsonrpc and run it for a while and then turn it off, you will not
    # be able to get the data from eth_getLogs for that period of time.

    # httpFullNodeEnable = true
    # httpFullNodePort = 8545
    # httpSolidityEnable = true
    # httpSolidityPort = 8555
    # httpPBFTEnable = true
    # httpPBFTPort = 8565
  }

  # Disabled api list, it will work for http, rpc and pbft, both fullnode and soliditynode,
  # but not jsonrpc.
  # Sample: The setting is case insensitive, GetNowBlock2 is equal to getnowblock2
  #
  # disabledApi = [
  #   "getaccount",
  #   "getnowblock2"
  # ]
}
```

## Usage

### Deploying a FullNode

To deploy a FullNode, run the following command:

```shell
bash deploy_tron.sh --app FullNode --net mainnet
```

### Deploying a SolidityNode

To deploy a SolidityNode, run the following command:

```shell
bash deploy_tron.sh --app SolidityNode --net mainnet --trust-node <grpc-ip:grpc-port>
```

### Deploying FullNode and SolidityNode on the Same Host

To deploy both FullNode and SolidityNode on the same host, run the following commands:

```shell
bash deploy_tron.sh --app FullNode --net mainnet
bash deploy_tron.sh --app SolidityNode --net mainnet --trust-node 127.0.0.1:50051 --rpc-port 50041
```

### Deploying the gRPC Gateway

To deploy the gRPC gateway, run the following command:

```shell
bash deploy_grpc_gateway.sh
```

You can customize the deployment by providing additional parameters:

```shell
bash deploy_grpc_gateway.sh --rpchost 127.0.0.1 --rpcport 50052 --httpport 18891
```

## Troubleshooting

If you encounter any issues during the deployment process, here are some common troubleshooting tips:

1. **Check Dependencies**: Ensure that all required dependencies, such as `java`, `git`, and `wget`, are installed on your system. If any dependencies are missing, install them and try running the script again.

2. **Verify Network Connectivity**: If you experience network-related issues, such as failed downloads or connection timeouts, check your network connectivity and try again. You can also add retries for network-related commands in the script to handle temporary network issues.

3. **Validate Configuration Files**: Ensure that the configuration files are correctly set up and contain valid values. Double-check the paths, ports, and other settings in the configuration files.

4. **Check Logs**: Review the log files generated during the deployment process for any error messages or warnings. The log files can provide valuable information to help you identify and resolve issues.

5. **Increase Verbosity**: If the script does not provide enough information about the error, consider increasing the verbosity of the script by adding more detailed error messages and logging.

6. **Consult Documentation**: Refer to the official documentation and user guides for additional information and troubleshooting steps. The `docs` directory contains detailed documentation files, such as a user guide, developer guide, and API documentation.

## Additional Resources

For more information and resources, refer to the following:

- [TRON Protocol Documentation](https://developers.tron.network/docs)
- [TRON GitHub Repository](https://github.com/tronprotocol)
- [TRON Community Forum](https://forum.tron.network)
