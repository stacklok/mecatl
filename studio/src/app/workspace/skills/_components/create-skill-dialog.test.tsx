import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { zipSync } from "fflate";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { HarnessSkillUploadFile } from "@/lib/harness/client";
import { CreateSkillDialog } from "./create-skill-dialog";

/**
 * Pins the create dialog: the chooser (upload / manual), the upload path
 * creating IMMEDIATELY (never the editor — a .md goes through the legacy
 * body create, a .zip through the multi-file create with the wrapper folder
 * stripped), picker-cancel closing the dialog, the manual editor's submit
 * gate (grammar-valid name + non-empty body), and a controller refusal
 * rendering verbatim while the dialog stays open.
 */

const create = vi.fn<(name: string, body: string) => Promise<void>>(() =>
  Promise.resolve(),
);
const createFiles = vi.fn<
  (name: string, files: HarnessSkillUploadFile[]) => Promise<void>
>(() => Promise.resolve());
const onCreated = vi.fn<(name: string) => void>();

beforeEach(() => {
  create.mockClear();
  create.mockImplementation(() => Promise.resolve());
  createFiles.mockClear();
  createFiles.mockImplementation(() => Promise.resolve());
  onCreated.mockClear();
});

async function openDialog(user: ReturnType<typeof userEvent.setup>) {
  render(
    <CreateSkillDialog
      create={create}
      createFiles={createFiles}
      onCreated={onCreated}
    />,
  );
  await user.click(screen.getByRole("button", { name: /New skill/ }));
  return screen.findByRole("dialog");
}

/** Chooser → Create manually → Next, landing on the editor step. */
async function openManualEditor(user: ReturnType<typeof userEvent.setup>) {
  await openDialog(user);
  await user.click(screen.getByRole("button", { name: /Create manually/ }));
  await user.click(screen.getByRole("button", { name: "Next" }));
}

describe("create skill dialog", () => {
  it("opens on the chooser: two cards, pickers, no editor", async () => {
    const user = userEvent.setup();
    await openDialog(user);

    expect(
      screen.getByRole("button", { name: /Import a SKILL\.md/ }),
    ).toBeTruthy();
    expect(
      screen.getByRole("button", { name: /Create manually/ }),
    ).toBeTruthy();
    expect(screen.getByRole("button", { name: "Upload file" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Upload folder" })).toBeTruthy();
    expect(screen.queryByText(/restarts the daemon/)).toBeNull();
    expect(
      screen.queryByRole("textbox", { name: "SKILL.md content" }),
    ).toBeNull();
  });

  it("creates immediately from an uploaded SKILL.md — no editor step", async () => {
    const user = userEvent.setup();
    await openDialog(user);

    const content = "---\nname: Uploaded Helper\n---\n# Do the thing\n";
    await user.upload(
      screen.getByLabelText("Upload a SKILL.md or zip file"),
      new File([content], "Some Notes.md", { type: "text/markdown" }),
    );

    await waitFor(() => expect(create).toHaveBeenCalledTimes(1));
    const [name, body] = create.mock.calls[0];
    expect(name).toBe("uploaded-helper");
    expect(body).toBe(content);
    expect(
      screen.queryByRole("textbox", { name: "SKILL.md content" }),
    ).toBeNull();
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(onCreated).toHaveBeenCalledWith("uploaded-helper");
  });

  it("creates a folder skill from a zip, stripping the wrapper folder", async () => {
    const user = userEvent.setup();
    await openDialog(user);

    const encoder = new TextEncoder();
    const zipped = zipSync({
      "my-skill/SKILL.md": encoder.encode("---\nname: zipped\n---\nbody"),
      "my-skill/scripts/run.sh": encoder.encode("echo hi\n"),
      "__MACOSX/my-skill/._SKILL.md": encoder.encode("junk"),
    });
    await user.upload(
      screen.getByLabelText("Upload a SKILL.md or zip file"),
      new File([Buffer.from(zipped)], "my-skill.zip", {
        type: "application/zip",
      }),
    );

    await waitFor(() => expect(createFiles).toHaveBeenCalledTimes(1));
    const [name, files] = createFiles.mock.calls[0];
    expect(name).toBe("zipped");
    expect(files.map((f) => f.path).sort()).toEqual([
      "SKILL.md",
      "scripts/run.sh",
    ]);
    const skillMd = files.find((f) => f.path === "SKILL.md");
    expect(atob(skillMd?.contentBase64 ?? "")).toBe(
      "---\nname: zipped\n---\nbody",
    );
    expect(create).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(onCreated).toHaveBeenCalledWith("zipped");
  });

  it("refuses a zip with no root SKILL.md, staying on the chooser", async () => {
    const user = userEvent.setup();
    await openDialog(user);

    const zipped = zipSync({
      "notes/readme.md": new TextEncoder().encode("nope"),
    });
    await user.upload(
      screen.getByLabelText("Upload a SKILL.md or zip file"),
      new File([Buffer.from(zipped)], "notes.zip", {
        type: "application/zip",
      }),
    );

    expect(await screen.findByText(/needs a SKILL\.md/)).toBeTruthy();
    expect(createFiles).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog")).toBeTruthy();
  });

  it("closes the dialog when the picker is cancelled", async () => {
    const user = userEvent.setup();
    await openDialog(user);

    fireEvent(
      screen.getByLabelText("Upload a SKILL.md or zip file"),
      new Event("cancel"),
    );
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(create).not.toHaveBeenCalled();
  });

  it("renders an upload refusal from the controller and stays open", async () => {
    create.mockImplementation(() =>
      Promise.reject(new Error('A skill named "my-skill" already exists')),
    );
    const user = userEvent.setup();
    await openDialog(user);

    await user.upload(
      screen.getByLabelText("Upload a SKILL.md or zip file"),
      new File(["---\nname: my-skill\n---\nbody"], "my-skill.md", {
        type: "text/markdown",
      }),
    );

    expect(
      await screen.findByText('A skill named "my-skill" already exists'),
    ).toBeTruthy();
    expect(screen.getByRole("dialog")).toBeTruthy();
    expect(onCreated).not.toHaveBeenCalled();
  });

  it("manual mode seeds the template with no upload controls", async () => {
    const user = userEvent.setup();
    await openManualEditor(user);

    const body = screen.getByRole("textbox", { name: "SKILL.md content" });
    expect((body as HTMLTextAreaElement).value).toContain("description:");
    expect(screen.queryByRole("button", { name: /Upload file/ })).toBeNull();

    // Name empty → invalid → the gate holds and the rule shows as helper text.
    expect(
      (
        screen.getByRole("button", {
          name: "Create skill",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true);
    expect(screen.getByText(/Lowercase letters, digits/)).toBeTruthy();
  });

  it("keeps Create disabled while the name breaks the grammar", async () => {
    const user = userEvent.setup();
    await openManualEditor(user);

    await user.type(screen.getByRole("textbox", { name: "Name" }), "Bad Name");
    expect(
      (
        screen.getByRole("button", {
          name: "Create skill",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true);
    expect(create).not.toHaveBeenCalled();
  });

  it("creates manually with the typed name and body, then reports the name", async () => {
    const user = userEvent.setup();
    await openManualEditor(user);

    await user.type(screen.getByRole("textbox", { name: "Name" }), "my-skill");
    expect(screen.queryByText(/Lowercase letters, digits/)).toBeNull();
    await user.click(screen.getByRole("button", { name: "Create skill" }));

    expect(create).toHaveBeenCalledTimes(1);
    const [name, body] = create.mock.calls[0];
    expect(name).toBe("my-skill");
    expect(body).toContain("description:");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(onCreated).toHaveBeenCalledWith("my-skill");
  });

  it("renders a manual-create refusal verbatim and stays open", async () => {
    create.mockImplementation(() =>
      Promise.reject(new Error('A skill named "my-skill" already exists')),
    );
    const user = userEvent.setup();
    await openManualEditor(user);

    await user.type(screen.getByRole("textbox", { name: "Name" }), "my-skill");
    await user.click(screen.getByRole("button", { name: "Create skill" }));

    expect(
      await screen.findByText('A skill named "my-skill" already exists'),
    ).toBeTruthy();
    expect(screen.getByRole("dialog")).toBeTruthy();
    expect(onCreated).not.toHaveBeenCalled();
  });
});
