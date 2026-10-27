**🚨 Real issue** · High confidence · Sidekick investigated \*\*urgent\*\* \<img src\=x onerror\=alert\(1\)\> \@channel

See \[the fix\]\(https\://evil.example.com\) and \!\[x\]\(https\://evil.example.com/p.png\)
\# Heading
1\. not a list
\> quote \| table \| \~\~gone\~\~ \:tada\: \&amp; \`code\` \\

**Likely cause**

Deploy 4f2a of checkout\-api removed a query cache.

**Impact**

Checkout p99 is 2.8s for about 30% of requests.

**Findings**

- line one \- injected item · [\]\(javascript\:alert\(1\)\) \[x](https://signoz.example.com/q/%281%29)

**Proposed fixes**

1. **\_italic\_ fix**\: \* also 2. this

_Checked signoz, github_