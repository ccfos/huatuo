# Copyright 2026 The HuaTuo Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

def grafana_profiling_url($grafana; $region):
  $grafana + "/d/continuous-profiling?var-region=" + ($region | @uri)
  + "&var-hostname=" + (.hostname | @uri)
  + "&var-task_id=" + (.request_id | @uri)
  + "&var-scope=" + (
    (if .scope == "host" then
      "NOT _exists_:container_id"
    else
      "_exists_:container_id"
    end) | @uri
  )
  + (if .scope == "container" then
    "&var-container_id=" + (.container_id | @uri)
  else
    ""
  end)
  + "&var-type=" + (.profile_type | @uri)
  + "&from=" + (.time_range.from_ms | tostring)
  + "&to=" + (.time_range.to_ms | tostring);
