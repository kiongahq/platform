import { Icon } from "./Icon";
type Branch = {
  name: string;
  path: string;
  children: Map<string, Branch>;
  file: boolean;
};
/** A semantic directory tree for generated file manifests. */
export function FileTree({ paths }: { paths: string[] }) {
  const root: Branch = { name: "", path: "", children: new Map(), file: false };
  for (const path of [...new Set(paths)].sort()) {
    const parts = path.split("/").filter(Boolean);
    let parent = root;
    parts.forEach((name, index) => {
      if (!parent.children.has(name))
        parent.children.set(name, {
          name,
          path: parts.slice(0, index + 1).join("/"),
          children: new Map(),
          file: false,
        });
      parent = parent.children.get(name)!;
      if (index === parts.length - 1) parent.file = true;
    });
  }
  function branches(node: Branch) {
    return (
      <ul>
        {[...node.children.values()]
          .sort(
            (a, b) =>
              Number(!!b.children.size) - Number(!!a.children.size) ||
              a.name.localeCompare(b.name),
          )
          .map((child) => (
            <li key={child.path}>
              {child.children.size ? (
                <details open>
                  <summary>
                    <Icon name="folder" />
                    <span>{child.name}</span>
                  </summary>
                  {branches(child)}
                </details>
              ) : (
                <span className="file-tree-leaf">
                  <Icon name="code" />
                  <span>{child.name}</span>
                </span>
              )}
            </li>
          ))}
      </ul>
    );
  }
  return (
    <div className="file-tree" aria-label="Produced file tree">
      {branches(root)}
    </div>
  );
}
