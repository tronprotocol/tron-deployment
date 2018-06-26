APP="FullNode"
PROJECT="java-tron"
WORK_SPACE=$PWD
NET="mainnet"
BRANCH="master"
DB="keep"
RPC_PORT=50051

while [ -n "$1" ] ;do
    case "$1" in
        --net)
            NET=$2
            shift 2
            ;;
        --app)
            APP=$2
            shift 2 ;;
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
        *)
            ;;
    esac
    shift
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
  COMMITID="07236ce59d39f2e2d9e574e81e81d5cd682a08cb"
elif [ $NET == "testnet" ]; then
  wget https://raw.githubusercontent.com/tronprotocol/TronDeployment/master/test_net_config.conf -O test_net_config.conf
  CONF_PATH=$BIN_PATH/test_net_config.conf
fi

if [ -n $RPC_PORT ]; then
  sed -i "s/port = 50051/needSyncCheck = $RPC_PORT/g" $CONF_PATH
fi 
# checkout branch or commitid
if [ -n  "$BRANCH" ]; then
  cd $BIN_PATH/$PROJECT && git fetch && git checkout -t origin/$BRANCH;  git reset --hard origin/$BRANCH
fi

if [ -n "$COMMITID" ]; then
  cd $BIN_PATH/$PROJECT && git fetch && git checkout $COMMITID
fi

if [ $DB == "remove" ]; then
  rm -rf $BIN_PATH/output-directory
elif [ $DB == "backup" ]; then
  tar -czf $BIN_PATH/output-directory-$GIT_COMMIT-$NET.tar.gz $BIN_PATH/output-directory
  rm -rf $BIN_PATH/output-directory
fi

cd $BIN_PATH/$PROJECT && ./gradlew build -x test
cp $BIN_PATH/$PROJECT/build/libs/$JAR_NAME.jar $BIN_PATH


if [ $APP == "SolidityNode" ]; then
  START_OPT="--trust-node $TRUST_NODE"
elif [ $APP == "FullNode" ]; then
  START_OPT=""
fi

pid=`ps -ef |grep $JAR_NAME |grep -v grep |awk '{print $2}'`
if [ $pid ]; then
  kill -15 $pid
  echo "kill -15 $APP"
fi
echo `date` >> start.log

logtime=`date +%Y-%m-%d_%H-%M-%S`


cd $BIN_PATH
nohup java -jar "$JAR_NAME.jar" $START_OPT -c $CONF_PATH  >> start.log 2>&1 &
pid=`ps -ef |grep $JAR_NAME |grep -v grep |awk '{print $2}'`


echo "start $APP with pid: $pid on $HOSTNAME"
echo "process: nohup java -jar $JAR_NAME.jar $START_OPT -c $CONF_PATH  >> start.log 2>&1 &"
echo "deploy path : $BIN_PATH"
echo "git commit : "`git rev-parse HEAD`
echo "git branch : $BRANCH"
echo "database path : $BIN_PATH/output-directory"
echo "log path : $BIN_PATH/logs"
echo "grpc port : $RPC_PORT"
if [ $APP == "SolidityNode" ]; then
  echo "trust-node : $TRUST_NODE"
fi

