import Markdown, { type Components } from "react-markdown";
import remarkBreaks from "remark-breaks";
import remarkGfm from "remark-gfm";

// Manifest text is Markdown with GitHub tables, written with single line
// breaks meant as breaks. Raw HTML is never rendered, images are dropped
// (they would load from the internet), and links open in a new window.
const components: Components = {
  a: ({ node: _node, ...props }) => <a {...props} target="_blank" rel="noopener noreferrer" />,
};

export default function AppMarkdown({ children }: { children: string }) {
  return (
    <Markdown remarkPlugins={[remarkGfm, remarkBreaks]} disallowedElements={["img"]} unwrapDisallowed components={components}>
      {children}
    </Markdown>
  );
}
