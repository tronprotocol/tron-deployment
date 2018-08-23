#!/bin/bash
APP="FullNode"
PROJECT="java-tron"
WORK_SPACE=$PWD
NET="mainnet"
BRANCH="master"
DB="keep"
RPC_PORT=50051
TRUST_NODE="127.0.0.1:50051"

if [ ! -f ticktick.sh ];then
  wget https://raw.githubusercontent.com/kristopolous/TickTick/master/ticktick.sh -O ticktick.sh
fi

source ticktick.sh
CODE_VERSION=`curl "https://api.github.com/repos/tronprotocol/java-tron/releases/latest"`
tickParse "$CODE_VERSION"
tag_name=``tag_name``
rm ticktick.sh
BRANCH=$tag_name
echo current code branch $BRANCH

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
        *)
            ;;
    esac
done

if [ $APP == "Witness" ]; then
  JAR_NAME="FullNode"
else
  JAR_NAME=$APP
fi

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
  CONF_PATH=$BIN_PATH/main_net_config.conf
elif [ $NET == "testnet" ]; then
  wget https://raw.githubusercontent.com/tronprotocol/TronDeployment/master/test_net_config.conf -O test_net_config.conf
  BRANCH="develop"
  CONF_PATH=$BIN_PATH/test_net_config.conf
elif [ $NET == "privatenet" ]; then
  wget https://raw.githubusercontent.com/tronprotocol/TronDeployment/master/private_net_config.conf -O private_net_config.conf
  CONF_PATH=$BIN_PATH/private_net_config.conf
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

count=1
while [ $count -le 60 ]; do
  pid=`ps -ef |grep  $JAR_NAME.jar | grep java | grep -v grep |awk '{print $2}'`
  if [ -n "$pid" ]; then
    kill -15 $pid
    echo "kill -15 java-tron, counter $count"
    sleep 1
  else
    echo "$APP killed"
    break
  fi
  count=$[$count+1]
  if [ $count -ge 60 ]; then
    kill -9 $pid
  fi
done

echo "starting $APP"
cd $BIN_PATH
if [ $NET == "mainnet" ]; then
  nohup java -jar "$JAR_NAME.jar" $START_OPT -c $CONF_PATH  >> start.log 2>&1 &
  echo "process : nohup java -jar $JAR_NAME.jar $START_OPT -c $CONF_PATH  >> start.log 2>&1 &"
elif [ $NET == "testnet" ]; then
  nohup java -jar "$JAR_NAME.jar" $START_OPT -c $CONF_PATH  >> start.log 2>&1 &
  echo "process : nohup java -jar $JAR_NAME.jar $START_OPT -c $CONF_PATH  >> start.log 2>&1 &"
elif [ $NET == "privatenet" ]; then
  nohup java -jar "$JAR_NAME.jar" $START_OPT -c $CONF_PATH  --witness>> start.log 2>&1 &
  echo "process : nohup java -jar $JAR_NAME.jar $START_OPT -c $CONF_PATH --witness >> start.log 2>&1 &"
fi


pid=`ps ax |grep $JAR_NAME.jar |grep -v grep | awk '{print $1}'`
echo $pid

echo "application : $APP"
echo "pid : $pid"
echo "tron net : $NET"

echo "deploy path : $BIN_PATH"
echo "git commit : "`cd $BIN_PATH/$PROJECT; git rev-parse HEAD`
echo "git branch : $BRANCH"
echo "db path : $BIN_PATH/output-directory"
echo "log path : $BIN_PATH/logs"
echo "grpc port : $RPC_PORT"
if [ $APP == "SolidityNode" ]; then
  echo "trust-node : $TRUST_NODE"
fi
