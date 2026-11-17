package match

import "strings"

// nationalityRows is a closed lookup over a recorded fact, never an inference:
// each row lists the country names, then after the colon its adjectives. A
// country the table does not know adds nothing, and China maps to Chinese only
// so a recorded China can never select Taiwanese or Hong Konger. Names are ASCII
// so the table carries no accented spellings.
const nationalityRows = `
Afghanistan:Afghan
Albania:Albanian
Algeria:Algerian
Andorra:Andorran
Angola:Angolan
Antigua and Barbuda:Antiguan/Barbudan
Argentina:Argentine/Argentinian
Armenia:Armenian
Australia:Australian
Austria:Austrian
Azerbaijan:Azerbaijani
Bahamas|The Bahamas:Bahamian
Bahrain:Bahraini
Bangladesh:Bangladeshi
Barbados:Barbadian
Belarus:Belarusian
Belgium:Belgian
Belize:Belizean
Benin:Beninese
Bhutan:Bhutanese
Bolivia:Bolivian
Bosnia and Herzegovina|Bosnia-Herzegovina|Bosnia:Bosnian/Bosnian-Herzegovinian/Herzegovinian
Botswana:Motswana/Botswanan
Brazil:Brazilian
Brunei:Bruneian
Bulgaria:Bulgarian
Burkina Faso:Burkinabe
Burundi:Burundian
Cambodia:Cambodian
Cameroon:Cameroonian
Canada:Canadian
Cape Verde|Cabo Verde:Cape Verdean
Central African Republic:Central African
Chad:Chadian
Chile:Chilean
China|People's Republic of China|Mainland China|PRC:Chinese
Colombia:Colombian
Comoros:Comoran
Congo|Republic of the Congo:Congolese
Democratic Republic of the Congo|DR Congo|DRC:Congolese
Costa Rica:Costa Rican
Cote d'Ivoire|Ivory Coast:Ivorian
Croatia:Croatian
Cuba:Cuban
Cyprus:Cypriot
Czech Republic|Czechia:Czech
Denmark:Danish
Djibouti:Djiboutian
Dominica:Dominican
Dominican Republic:Dominican
Ecuador:Ecuadorian
Egypt:Egyptian
El Salvador:Salvadoran
Equatorial Guinea:Equatoguinean
Eritrea:Eritrean
Estonia:Estonian
Eswatini|Swaziland:Swazi
Ethiopia:Ethiopian
Fiji:Fijian
Finland:Finnish
France:French
Gabon:Gabonese
Gambia|The Gambia:Gambian
Georgia:Georgian
Germany:German
Ghana:Ghanaian
Greece:Greek
Grenada:Grenadian
Guatemala:Guatemalan
Guinea:Guinean
Guinea-Bissau:Bissau-Guinean
Guyana:Guyanese
Haiti:Haitian
Honduras:Honduran
Hong Kong:Hong Konger/Hongkonger
Hungary:Hungarian
Iceland:Icelandic
India:Indian
Indonesia:Indonesian
Iran:Iranian
Iraq:Iraqi
Ireland:Irish
Israel:Israeli
Italy:Italian
Jamaica:Jamaican
Japan:Japanese
Jordan:Jordanian
Kazakhstan:Kazakhstani/Kazakh
Kenya:Kenyan
Kiribati:I-Kiribati
Kosovo:Kosovar/Kosovan
Kuwait:Kuwaiti
Kyrgyzstan|Kyrgyz Republic:Kyrgyzstani/Kyrgyz
Laos:Lao/Laotian
Latvia:Latvian
Lebanon:Lebanese
Lesotho:Basotho/Mosotho
Liberia:Liberian
Libya:Libyan
Liechtenstein:Liechtensteiner
Lithuania:Lithuanian
Luxembourg:Luxembourgish/Luxembourger
Macau|Macao:Macanese
Madagascar:Malagasy
Malawi:Malawian
Malaysia:Malaysian
Maldives:Maldivian
Mali:Malian
Malta:Maltese
Marshall Islands:Marshallese
Mauritania:Mauritanian
Mauritius:Mauritian
Mexico:Mexican
Micronesia:Micronesian
Moldova:Moldovan
Monaco:Monegasque/Monacan
Mongolia:Mongolian
Montenegro:Montenegrin
Morocco:Moroccan
Mozambique:Mozambican
Myanmar|Burma:Burmese/Myanma
Namibia:Namibian
Nauru:Nauruan
Nepal:Nepali/Nepalese
Netherlands|The Netherlands|Holland:Dutch
New Zealand:New Zealander
Nicaragua:Nicaraguan
Niger:Nigerien
Nigeria:Nigerian
North Korea|Democratic People's Republic of Korea|DPRK:North Korean
North Macedonia|Macedonia:Macedonian/North Macedonian
Norway:Norwegian
Oman:Omani
Pakistan:Pakistani
Palau:Palauan
Palestine:Palestinian
Panama:Panamanian
Papua New Guinea:Papua New Guinean
Paraguay:Paraguayan
Peru:Peruvian
Philippines|The Philippines:Filipino/Philippine
Poland:Polish
Portugal:Portuguese
Qatar:Qatari
Romania:Romanian
Russia|Russian Federation:Russian
Rwanda:Rwandan
Saint Kitts and Nevis:Kittitian/Nevisian
Saint Lucia:Saint Lucian
Saint Vincent and the Grenadines:Vincentian
Samoa:Samoan
San Marino:Sammarinese
Sao Tome and Principe:Sao Tomean
Saudi Arabia:Saudi/Saudi Arabian
Senegal:Senegalese
Serbia:Serbian
Seychelles:Seychellois
Sierra Leone:Sierra Leonean
Singapore:Singaporean
Slovakia|Slovak Republic:Slovak/Slovakian
Slovenia:Slovenian/Slovene
Solomon Islands:Solomon Islander
Somalia:Somali
South Africa:South African
South Korea|Republic of Korea:South Korean
South Sudan:South Sudanese
Spain:Spanish
Sri Lanka:Sri Lankan
Sudan:Sudanese
Suriname:Surinamese
Sweden:Swedish
Switzerland:Swiss
Syria:Syrian
Taiwan:Taiwanese
Tajikistan:Tajikistani/Tajik
Tanzania:Tanzanian
Thailand:Thai
Timor-Leste|East Timor:Timorese
Togo:Togolese
Tonga:Tongan
Trinidad and Tobago:Trinidadian/Tobagonian
Tunisia:Tunisian
Turkey:Turkish
Turkmenistan:Turkmen
Tuvalu:Tuvaluan
Uganda:Ugandan
Ukraine:Ukrainian
United Arab Emirates|UAE:Emirati
United Kingdom|UK|Great Britain|Britain:British
United States|United States of America|USA|US|U.S.|U.S.A.:American
Uruguay:Uruguayan
Uzbekistan:Uzbekistani/Uzbek
Vanuatu:Ni-Vanuatu
Venezuela:Venezuelan
Vietnam|Viet Nam:Vietnamese
Yemen:Yemeni
Zambia:Zambian
Zimbabwe:Zimbabwean
`

var nationalityByCountry = buildNationalityIndex()

func nationalityKey(country string) string {
	return strings.Join(strings.Fields(strings.ToLower(country)), " ")
}

func buildNationalityIndex() map[string][]string {
	idx := map[string][]string{}
	for _, row := range strings.Split(strings.TrimSpace(nationalityRows), "\n") {
		names, adjectives, _ := strings.Cut(row, ":")
		adj := strings.Split(adjectives, "/")
		for _, name := range strings.Split(names, "|") {
			idx[nationalityKey(name)] = adj
		}
	}
	return idx
}

// nationalityAdjectives returns the adjectives a nationality picker lists for a
// recorded country, or nil for a country the table does not know. It applies to
// the recorded citizenship only, never to residence, address or name entries.
func nationalityAdjectives(country string) []string {
	return nationalityByCountry[nationalityKey(country)]
}

// expandCitizenshipSearch is the recorded countries followed by each country's
// adjectives, without duplicates, recorded values first.
func expandCitizenshipSearch(countries []string) []string {
	out := append([]string(nil), countries...)
	seen := map[string]bool{}
	for _, c := range out {
		seen[c] = true
	}
	for _, c := range countries {
		for _, a := range nationalityAdjectives(c) {
			if !seen[a] {
				seen[a] = true
				out = append(out, a)
			}
		}
	}
	return out
}
