# Prettytail Fixture

## Overview

This is a **plausible-fake** sample document used to exercise the markdown
renderer. It has *emphasis*, `inline code`, and a [link to nowhere](https://example.com/prettytail).

- first bullet item
- second bullet item
- third bullet item

1. first numbered step
2. second numbered step
3. third numbered step

> A blockquote reminding us that pretty-printed logs beat raw ones.

```go
func Greet(name string) string {
	return "hello, " + name
}
```

| Format  | Detected By      | Notes                |
|---------|-------------------|-----------------------|
| md      | extension/content | glamour renders it   |
| ndjson  | content sniff     | one object per line  |
| logfmt  | content sniff     | key=value pairs      |
