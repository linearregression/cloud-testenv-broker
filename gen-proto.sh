#!/bin/bash

# Copyright 2014 Google Inc. All Rights Reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.


SRC=$GOPATH/src
rm -Rf $SRC/google

echo "GO: broker protos"
protoc -I protos \
  protos/google/emulators/broker.proto \
  -I . --go_out=plugins=grpc:$SRC

echo "GO: protobuf"
protoc -I protos \
  protos/google/protobuf/*.proto \
  -I . --go_out=plugins=grpc:$GOPATH/src

