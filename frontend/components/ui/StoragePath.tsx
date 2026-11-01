import { Icon } from "./Icon";
export function storageHref(bucket: string, prefix = "", key = "") {
  return `/console/storage?${new URLSearchParams({ bucket, ...(prefix ? { prefix } : {}), ...(key ? { key } : {}) })}`;
}
export function StoragePath({ value }: { value: string }) {
  if (!value.startsWith("s3://")) return <span>{value}</span>;
  const parts = value.slice(5).split("/"),
    bucket = parts.shift() || "";
  const segments = parts.filter(Boolean).map((segment) => {
    try {
      return decodeURIComponent(segment);
    } catch {
      return segment;
    }
  });
  return (
    <span className="storage-path-link">
      <Icon name="bucket" />
      <code>s3://</code>
      <a href={storageHref(bucket)}>{bucket}</a>
      {segments.map((segment, index) => {
        const path = segments.slice(0, index + 1).join("/"),
          object = index === segments.length - 1 && !value.endsWith("/");
        return (
          <span className="storage-crumb" key={index}>
            <span aria-hidden="true">/</span>
            <a
              href={
                object
                  ? storageHref(bucket, "", path)
                  : storageHref(bucket, path + "/")
              }
              title={`s3://${bucket}/${path}${object ? "" : "/"}`}
            >
              {segment}
            </a>
          </span>
        );
      })}
    </span>
  );
}
