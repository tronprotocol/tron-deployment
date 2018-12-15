#!/bin/bash

nohup java -jar java-tron.jar -w -p $witness -c config.conf 2>&1 >>shell.log &
