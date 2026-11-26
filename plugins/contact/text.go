package main

import "strings"

// langKey is the language the plugin has texts in for a language code,
// and the suffix of its per-language settings: "es" ("es", "es-MX",
// "es_AR"), "ca" or "en".
func langKey(lang string) string {
	l := strings.ToLower(strings.TrimSpace(lang))
	if i := strings.IndexAny(l, "-_"); i >= 0 {
		l = l[:i]
	}
	switch l {
	case "es", "ca":
		return l
	}
	return "en"
}

// strings shown to visitors (form lang) and to the recipient (site lang).
type text struct {
	privacyLead, privacyLink                             string
	nameShort, nameLong, emailInvalid, subjectLong       string
	messageShort, messageLong, noLinks, tooManyLinks     string
	consent, checkForm, success, rateLimited, sendFailed string
	mailIntro, mailName, mailEmail, mailSubject          string
	mailPage, mailLang, mailConsent, mailYes             string
	mailMessage, mailReply, mailNoSubject                string
}

var english = text{
	privacyLead:   "How we handle your data:",
	privacyLink:   "Privacy policy",
	nameShort:     "Enter your name (at least 2 characters).",
	nameLong:      "The name is too long (at most 100 characters).",
	emailInvalid:  "Enter a valid e-mail address.",
	subjectLong:   "The subject is too long (at most 150 characters).",
	messageShort:  "Write at least %d characters.",
	messageLong:   "The message is too long (at most 5000 characters).",
	noLinks:       "Links are not allowed in the message.",
	tooManyLinks:  "Too many links (at most %d).",
	consent:       "Accept the privacy policy to send the message.",
	checkForm:     "Check the highlighted fields.",
	success:       "Thanks, {name}! Your message was sent. We will reply to {email}.",
	rateLimited:   "Too many messages right now. Try again later.",
	sendFailed:    "The message could not be sent. Try again later.",
	mailIntro:     "New message from the contact form.",
	mailName:      "Name",
	mailEmail:     "E-mail",
	mailSubject:   "Subject",
	mailPage:      "Page",
	mailLang:      "Language",
	mailConsent:   "Privacy consent",
	mailYes:       "yes",
	mailMessage:   "Message",
	mailReply:     "Reply to this e-mail to answer %s.",
	mailNoSubject: "Message from %s",
}

var spanishText = text{
	privacyLead:   "Cómo tratamos tus datos:",
	privacyLink:   "Política de privacidad",
	nameShort:     "Escribe tu nombre (al menos 2 caracteres).",
	nameLong:      "El nombre es demasiado largo (100 caracteres como máximo).",
	emailInvalid:  "Escribe una dirección de correo válida.",
	subjectLong:   "El asunto es demasiado largo (150 caracteres como máximo).",
	messageShort:  "Escribe al menos %d caracteres.",
	messageLong:   "El mensaje es demasiado largo (5000 caracteres como máximo).",
	noLinks:       "El mensaje no puede contener enlaces.",
	tooManyLinks:  "Demasiados enlaces (%d como máximo).",
	consent:       "Acepta la política de privacidad para enviar el mensaje.",
	checkForm:     "Revisa los campos marcados.",
	success:       "¡Gracias, {name}! Tu mensaje se ha enviado. Te responderemos a {email}.",
	rateLimited:   "Ahora mismo hay demasiados mensajes. Inténtalo más tarde.",
	sendFailed:    "No se pudo enviar el mensaje. Inténtalo más tarde.",
	mailIntro:     "Nuevo mensaje del formulario de contacto.",
	mailName:      "Nombre",
	mailEmail:     "Correo",
	mailSubject:   "Asunto",
	mailPage:      "Página",
	mailLang:      "Idioma",
	mailConsent:   "Consentimiento de privacidad",
	mailYes:       "sí",
	mailMessage:   "Mensaje",
	mailReply:     "Responde a este correo para contestar a %s.",
	mailNoSubject: "Mensaje de %s",
}

var catalanText = text{
	privacyLead:   "Com tractem les teves dades:",
	privacyLink:   "Política de privacitat",
	nameShort:     "Escriu el teu nom (com a mínim 2 caràcters).",
	nameLong:      "El nom és massa llarg (100 caràcters com a màxim).",
	emailInvalid:  "Escriu una adreça de correu vàlida.",
	subjectLong:   "L'assumpte és massa llarg (150 caràcters com a màxim).",
	messageShort:  "Escriu com a mínim %d caràcters.",
	messageLong:   "El missatge és massa llarg (5000 caràcters com a màxim).",
	noLinks:       "El missatge no pot contenir enllaços.",
	tooManyLinks:  "Massa enllaços (%d com a màxim).",
	consent:       "Accepta la política de privacitat per enviar el missatge.",
	checkForm:     "Revisa els camps marcats.",
	success:       "Gràcies, {name}! El teu missatge s'ha enviat. Et respondrem a {email}.",
	rateLimited:   "Ara mateix hi ha massa missatges. Torna-ho a provar més tard.",
	sendFailed:    "No s'ha pogut enviar el missatge. Torna-ho a provar més tard.",
	mailIntro:     "Missatge nou del formulari de contacte.",
	mailName:      "Nom",
	mailEmail:     "Correu",
	mailSubject:   "Assumpte",
	mailPage:      "Pàgina",
	mailLang:      "Idioma",
	mailConsent:   "Consentiment de privacitat",
	mailYes:       "sí",
	mailMessage:   "Missatge",
	mailReply:     "Respon aquest correu per contestar a %s.",
	mailNoSubject: "Missatge de %s",
}

func texts(lang string) *text {
	switch langKey(lang) {
	case "es":
		return &spanishText
	case "ca":
		return &catalanText
	}
	return &english
}
