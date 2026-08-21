{{- define "huatuo.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "huatuo.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "huatuo.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "huatuo.selectorLabels" -}}
app.kubernetes.io/name: {{ include "huatuo.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app: huatuo
{{- end -}}

{{- define "huatuo.labels" -}}
helm.sh/chart: {{ include "huatuo.chart" . }}
{{ include "huatuo.selectorLabels" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "huatuo.configName" -}}
{{- default (printf "%s-config" (include "huatuo.fullname" .)) .Values.config.name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
