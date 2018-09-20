#!/bin/bash

wget https://raw.githubusercontent.com/tronprotocol/TronDeployment/master/Benchmark.jar -O Benchmark.jar
java -jar Benchmark.jar

if [ $? == 1 ]; then
 echo "Start fail! Please upgrade server configuration." 
 exit 1
fi
