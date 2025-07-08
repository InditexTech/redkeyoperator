# The following patch enables a conversion webhook for the CRD
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: redisclusters.redis.inditex.dev
spec:
  conversion:
    strategy: Webhook
    webhook:
      clientConfig:
        caBundle: WEBHOOK_CA_CERT
        service:
          namespace: redis-operator-webhook
          name: redis-operator-webhook-service
          path: /convert
      conversionReviewVersions:
      - v1
