import type { LucideIcon } from "lucide-react";
import { forwardRef, type ButtonHTMLAttributes } from "react";

type ButtonVariant = "primary" | "secondary" | "ghost" | "danger";

// Button is the dashboard's button. A plain <button> already looks like the
// secondary variant, so this is for the other variants, sizes and icons.
//
//   <Button variant="primary" icon={Plus} onClick={add}>Add application</Button>
//   <Button variant="ghost" size="sm" icon={X} className="icon-only" aria-label="Close" />
export const Button = forwardRef<HTMLButtonElement, ButtonHTMLAttributes<HTMLButtonElement> & { variant?: ButtonVariant; size?: "sm" | "md"; icon?: LucideIcon }>(function Button(
  { variant = "secondary", size = "md", icon: Icon, className, children, type = "button", ...rest },
  ref,
) {
  const classes = [variant === "secondary" ? "" : variant, size === "sm" ? "small" : "", className ?? ""].filter(Boolean).join(" ");
  return (
    <button ref={ref} type={type} className={classes || undefined} {...rest}>
      {Icon && <Icon />}
      {children}
    </button>
  );
});
