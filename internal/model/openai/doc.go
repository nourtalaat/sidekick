// Package openai is the model connector for the OpenAI API and for servers
// that speak it, such as Ollama, vLLM and the llama.cpp server. It
// registers itself as the model provider "openai".
//
// Without a base URL it calls the Responses API at api.openai.com; with one
// it calls Chat Completions at that address, and the api setting picks
// either. Known OpenAI models and the local model presets carry their
// capabilities, so they need no capabilities block. A preset gets a forced
// tool choice only on a server known to honour it.
//
// The package also registers the model provider "azure", for model
// deployments on Azure OpenAI and Microsoft Foundry. It uses the same
// connector on Azure's v1 API, with the deployment name as the model and
// an api-key header or a Microsoft Entra ID token as the credentials.
package openai
