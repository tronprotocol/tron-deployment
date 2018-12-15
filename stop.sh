#!/bin/bash

jps | grep jar | grep -v grep | awk '{print $1}' | xargs kill -9
