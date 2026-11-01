import { parseAllDocuments } from "yaml";
export type CodeLanguage =
  | "json"
  | "yaml"
  | "python"
  | "bash"
  | "javascript"
  | "typescript"
  | "sql"
  | "text"
  | string;
/** Format whitespace without rewriting JSON numeric/string lexemes. */
export function formatJSON(source: string) {
  JSON.parse(source);
  let result = "",
    indent = 0,
    quoted = false,
    escaped = false;
  const newline = () => {
    result = result.trimEnd() + "\n" + "  ".repeat(indent);
  };
  for (let i = 0; i < source.length; i++) {
    const char = source[i];
    if (quoted) {
      result += char;
      if (escaped) escaped = false;
      else if (char === "\\") escaped = true;
      else if (char === '"') quoted = false;
      continue;
    }
    if (char === '"') {
      quoted = true;
      result += char;
    } else if (char === "{" || char === "[") {
      result += char;
      indent++;
      const next = source.slice(i + 1).trimStart()[0];
      if (next !== "}" && next !== "]") newline();
    } else if (char === "}" || char === "]") {
      indent--;
      const prev = result.trimEnd().slice(-1);
      if (prev !== "{" && prev !== "[") newline();
      result += char;
    } else if (char === ",") {
      result += char;
      newline();
    } else if (char === ":") result += ": ";
    else if (!/\s/.test(char)) result += char;
  }
  return result;
}
export function formatCode(source: string, language: CodeLanguage) {
  if (language === "json") return formatJSON(source);
  if (language === "yaml" || language === "yml") {
    const documents = parseAllDocuments(source, {
      intAsBigInt: true,
      logLevel: "silent",
    });
    for (const document of documents)
      if (document.errors.length) throw new Error(document.errors[0].message);
    return documents
      .map((document, index) => {
        if (index > 0) document.directives.docStart = true;
        return document.toString({
          indent: 2,
          lineWidth: 0,
          collectionStyle: "block",
        });
      })
      .join("");
  }
  return source;
}
export function detectLanguage(source: string): CodeLanguage {
  if (/^\s*[\[{]/.test(source)) {
    try {
      JSON.parse(source);
      return "json";
    } catch {}
  }
  if (/^\s*(?:#.*\n)*[\w-]+:\s/m.test(source)) return "yaml";
  if (/^\s*(?:from \w[\w.]* import|import \w|def \w+\()/m.test(source))
    return "python";
  if (/^\s*(?:curl |#!.*(?:bash|sh))/m.test(source)) return "bash";
  if (/^\s*(?:const |let |export |import .* from )/m.test(source))
    return "typescript";
  return "text";
}
