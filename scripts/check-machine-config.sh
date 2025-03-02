#!/bin/bash

wget https://raw.githubusercontent.com/tronprotocol/TronDeployment/master/Benchmark.jar -O Benchmark.jar
java -jar Benchmark.jar

if [ $? == 1 ]; then
 echo "-----------------------------------------------------"
 echo "--------------------FAIL RESULT----------------------"
 echo "Start fail! Please upgrade server configuration."
 echo "CPU needs at least 16 cores"
 echo "Memory needs at least 30GB"
 echo "Java MUST be oracle jdk, and version >= 1.8"
 echo "-----------------------------------------------------" 
 exit 1
fi
