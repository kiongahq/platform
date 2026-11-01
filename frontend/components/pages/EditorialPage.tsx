import { StudioShell, type StudioView } from "@/components/studio/StudioShell";
import {
  PostsView,
  MediaView,
  AuthorsView,
  SettingsView,
} from "@/components/studio/StudioViews";
import { PostEditor } from "@/components/studio/PostEditor";
export function EditorialPage({ view = "posts" }: { view?: StudioView }) {
  return (
    <StudioShell view={view}>
      {view === "new" || view === "edit" ? (
        <PostEditor isNew={view === "new"} />
      ) : view === "media" ? (
        <MediaView />
      ) : view === "authors" ? (
        <AuthorsView />
      ) : view === "settings" ? (
        <SettingsView />
      ) : (
        <PostsView view={view} />
      )}
    </StudioShell>
  );
}
