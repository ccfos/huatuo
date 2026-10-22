// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

public class HeapFixture {
 static Object[] retained;
 static Payload[][] large = new Payload[6][];
 static byte[] pressure;
 static class Payload { long a,b,c,d,e,f,g,h; }
 public static void main(String[] args) throws Exception {
  int command;
  while ((command=System.in.read())!=-1) {
   if(command=='a') {
    retained=new Object[200000];
    for(int i=0;i<retained.length;i++) retained[i]=new Payload();
    for(int i=0;i<large.length;i++) large[i]=new Payload[300000];
    System.gc(); System.out.println("ready");
   } else if(command=='p') {
    pressure=new byte[64*1024*1024];
    for(int i=0;i<pressure.length;i+=4096) pressure[i]=1;
    System.out.println("pressure ready");
   } else if(command=='q') break;
  }
 }
}
