import { type StudioView } from "@/components/studio/StudioShell";
import {
  PostsView,
  MediaView,
  AuthorsView,
  SettingsView,
} from "@/components/studio/StudioViews";
import { PostEditor } from "@/components/studio/PostEditor";
export function EditorialPage({ view = "posts" }: { view?: StudioView }) {
  return (
    <>
      {view === "new" || view === "edit" ? (
        <PostEditor key={view} isNew={view === "new"} />
      ) : view === "media" ? (
        <MediaView />
      ) : view === "authors" ? (
        <AuthorsView />
      ) : view === "settings" ? (
        <SettingsView />
      ) : (
        <PostsView view={view} />
      )}
    </>
  );
}
