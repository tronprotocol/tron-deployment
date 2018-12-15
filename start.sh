#!/bin/bash

nohup java -jar FullNode.jar -w -p $witness -c config.conf 2>&1 >>shell.log &
