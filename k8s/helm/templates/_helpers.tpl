{{/*
Expand the name of the chart.
*/}}
{{- define "datavendor-payment-core.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "datavendor-payment-core.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "datavendor-payment-core.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "datavendor-payment-core.labels" -}}
helm.sh/chart: {{ include "datavendor-payment-core.chart" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: datavendor-payment-core
datavendor.com/environment: {{ .Values.global.environment | default "production" }}
datavendor.com/security-tier: "tier-0-critical"
{{- end }}

{{/*
Service-specific common labels helper
*/}}
{{- define "datavendor-payment-core.serviceLabels" -}}
{{- $root := index . 0 -}}
{{- $component := index . 1 -}}
{{ include "datavendor-payment-core.labels" $root }}
app.kubernetes.io/name: {{ $component }}
app.kubernetes.io/instance: {{ $root.Release.Name }}
app.kubernetes.io/component: {{ $component }}
tier: payment-core
{{- end }}

{{/*
Selector labels
*/}}
{{- define "datavendor-payment-core.selectorLabels" -}}
{{- $root := index . 0 -}}
{{- $component := index . 1 -}}
app.kubernetes.io/name: {{ $component }}
app.kubernetes.io/instance: {{ $root.Release.Name }}
app.kubernetes.io/component: {{ $component }}
{{- end }}
