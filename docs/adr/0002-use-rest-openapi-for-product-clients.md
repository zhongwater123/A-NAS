# Use REST and OpenAPI for product clients

A-NAS will expose its client-facing product API as versioned REST/JSON endpoints documented by OpenAPI, giving browser and future native clients a language-neutral, contract-testable interface while the product layer evolves. This decision does not select the Host Agent IPC protocol or require request/response HTTP for future event streaming.
