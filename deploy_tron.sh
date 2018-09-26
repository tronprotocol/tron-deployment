#!/bin/bash

# default config
APP="FullNode"
PROJECT="java-tron"
WORK_SPACE=$PWD
NET="mainnet"
BRANCH="master"
DB="keep"
RPC_PORT=50051
TRUST_NODE="127.0.0.1:50051"

# compute default heap size 
# total=`cat /proc/meminfo  |grep MemTotal |awk -F ' ' '{print $2}'`
# HEAP_SIZE=`echo "$total/1024/1024*0.8" | bc |awk -F. '{print $1"g"}'`


while [ -n "$1" ] ;do
    case "$1" in
        --net)
            NET=$2
            shift 2
            ;;
        --app)
            APP=$2
            shift 2 
            ;;
        --db)
            DB=$2
            shift 2 
            ;;
        --work_space)
            WORK_SPACE=$2
            shift 2
            ;;
        --commitid)
            COMMITID=$2
            shift 2
            ;;
        --branch)
            BRANCH=$2
            shift 2
            ;;
        --trust-node)
            TRUST_NODE=$2
            shift 2
            ;;
        --rpc-port)
            RPC_PORT=$2
            shift 2
            ;;
        --heap-size)
	    HEAP_SIZE=$2
	    shift 2
	    ;;
        *)
            ;;
    esac
done

if [ "$(uname)" == "Darwin" ]; then
    total=`top -l 1 | head -n 10 | grep PhysMem | awk -F " " '{a=substr($2,0,length($2)-1);b=substr($6,0,length($6)-1);if(match($2,/[0-9]+G/)) a=a*1024;if(match($6,/[0-9]+G/)) b=b*1024;print a+b}'`
    HEAP_SIZE=`echo "$total/1024*0.8" | bc |awk -F. '{print $1"g"}'`
elif [ "$(expr substr $(uname -s) 1 5)" == "Linux" ]; then
    total=`cat /proc/meminfo  |grep MemTotal |awk -F ' ' '{print $2}'`
    HEAP_SIZE=`echo "$total/1024/1024*0.8" | bc |awk -F. '{print $1"g"}'`
fi

if [ -z $HEAP_SIZE ]; then
	echo "should set heap size(mean jvm option: Xmx)"
	echo "for example: --heap-size 16384m"
	exit 2
fi

JAR_NAME=$APP

BIN_PATH="$WORK_SPACE/$APP"


CONF_PATH=""

echo 'download code from git repositorie'
if [ ! -e $BIN_PATH ]; then
  mkdir -p $BIN_PATH
  cd $BIN_PATH
  git clone https://github.com/tronprotocol/$PROJECT
fi

cd $BIN_PATH
if [ $NET == "mainnet" ]; then
  wget https://raw.githubusercontent.com/tronprotocol/TronDeployment/master/main_net_config.conf -O main_net_config.conf
  RELEASE=`curl --silent "https://api.github.com/repos/tronprotocol/java-tron/releases/latest" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/'`
  CONF_PATH=$BIN_PATH/main_net_config.conf
elif [ $NET == "testnet" ]; then
  wget https://raw.githubusercontent.com/tronprotocol/TronDeployment/master/test_net_config.conf -O test_net_config.conf
  BRANCH="master"
  CONF_PATH=$BIN_PATH/test_net_config.conf
fi

if [ -n $RPC_PORT ]; then
  sed "s/port = 50051/port = $RPC_PORT/g" $CONF_PATH > tmp
  cat tmp > $CONF_PATH
fi 
# checkout branch or commitid
if [ -n  "$BRANCH" ]; then
  cd $BIN_PATH/$PROJECT && git fetch && git checkout $BRANCH;  git reset --hard origin/$BRANCH
fi

if [ -n "$COMMITID" ]; then
  cd $BIN_PATH/$PROJECT && git fetch && git checkout $COMMITID
fi

if [ -n "$RELEASE" ]; then
  cd $BIN_PATH/$PROJECT && git fetch && git checkout tags/$RELEASE -b release
  BRANCH='release'
fi

if [ $DB == "remove" ]; then
  rm -rf $BIN_PATH/output-directory
  echo "remove db success"
elif [ $DB == "backup" ]; then
  tar -czf $BIN_PATH/output-directory-$GIT_COMMIT-$NET.tar.gz $BIN_PATH/output-directory
  rm -rf $BIN_PATH/output-directory
  echo "backup db success"
fi

cd $BIN_PATH/$PROJECT && ./gradlew build -x test
cp $BIN_PATH/$PROJECT/build/libs/$JAR_NAME.jar $BIN_PATH


if [ $APP == "SolidityNode" ]; then
  START_OPT="--trust-node $TRUST_NODE"
elif [ $APP == "FullNode" ]; then
  START_OPT=""
fi

JVM_OPT="-Xmx$HEAP_SIZE -XX:+HeapDumpOnOutOfMemoryError"

count=1
while [ $count -le 360 ]; do
  pid=`ps -ef |grep  $JAR_NAME.jar | grep java | grep -v grep |awk '{print $2}'`
  if [ -n "$pid" ]; then
    kill -15 $pid
    echo "kill -15 java-tron, counter $count"
    sleep 5
  else
    echo "$APP killed"
    break
  fi
  count=$[$count+1]
  if [ $count -ge 360 ]; then
    echo "deploy failure because of kill process fails!"
    exit 1
  fi
done

echo "starting $APP"
cd $BIN_PATH

nohup java $JVM_OPT -jar $JAR_NAME.jar -c $CONF_PATH $START_OPT >> start.log 2>&1 &

pid=`ps ax |grep $JAR_NAME.jar |grep -v grep | awk '{print $1}'`
echo $pid

if [ -z $pid ]; then
	echo "run $JAR_NAME failed, please check your parameters"
	exit 2
fi

echo "process    : nohup java $JVM_OPT -jar $JAR_NAME.jar $START_OPT -c $CONF_PATH  >> start.log 2>&1 &"
echo "pid        : $pid"
echo "application: $APP"
echo "tron net   : $NET"
echo "deploy path: $BIN_PATH"
echo "git commit : "`cd $BIN_PATH/$PROJECT; git rev-parse HEAD`
echo "git branch : $BRANCH"
echo "db path    : $BIN_PATH/output-directory"
echo "log path   : $BIN_PATH/logs"
echo "heap-size  : $HEAP_SIZE"
echo "grpc port  : $RPC_PORT"
if [ $APP == "SolidityNode" ]; then
	echo "trust-node : $TRUST_NODE"
fi
